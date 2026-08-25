package main

// Shared input-action parsing helpers (modifiers, hold, typing batches).
// Clean-room of Cursor ComputerUse X11Executor behaviors.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultTypingDelayMs   = 12
	defaultTypingBatchSize = 50
)

func splitModifiers(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, normalizeModifierName(p))
	}
	return out
}

func normalizeModifierName(name string) string {
	switch strings.ToLower(name) {
	case "ctrl", "control", "control_l", "control_r":
		return "ctrl"
	case "alt", "alt_l", "alt_r", "mod1":
		return "alt"
	case "shift", "shift_l", "shift_r":
		return "shift"
	case "super", "meta", "win", "windows", "cmd", "command":
		return "super"
	default:
		return strings.ToLower(name)
	}
}

func wrapWithModifiers(mods []string, body [][]string) [][]string {
	if len(mods) == 0 {
		return body
	}
	var out [][]string
	for _, m := range mods {
		out = append(out, []string{"keydown", "--", m})
	}
	out = append(out, body...)
	for i := len(mods) - 1; i >= 0; i-- {
		out = append(out, []string{"keyup", "--", mods[i]})
	}
	return out
}

// splitTypeSegments splits text on newlines (CRLF/CR/LF) into typed chunks
// separated by Return presses (Cursor ComputerUse type behavior).
func splitTypeSegments(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

func chunkString(s string, size int) []string {
	if size <= 0 {
		size = defaultTypingBatchSize
	}
	if s == "" {
		return []string{""}
	}
	var chunks []string
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= size && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
			n = 0
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

func buildTypeInvocations(text string, delayMs, batchSize int) [][]string {
	if delayMs < 0 {
		delayMs = defaultTypingDelayMs
	}
	if batchSize <= 0 {
		batchSize = defaultTypingBatchSize
	}
	segments := splitTypeSegments(text)
	var out [][]string
	for i, seg := range segments {
		if i > 0 {
			out = append(out, []string{"key", "--", "Return"})
		}
		if seg == "" {
			continue
		}
		for _, chunk := range chunkString(seg, batchSize) {
			out = append(out, []string{"type", "--delay", strconv.Itoa(delayMs), "--", chunk})
		}
	}
	if len(out) == 0 {
		out = append(out, []string{"type", "--delay", strconv.Itoa(delayMs), "--", ""})
	}
	return out
}

func buildKeyInvocations(key string, holdMs int) ([][]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("key requires a single key or chord, e.g. 'input key ctrl+s'")
	}
	if holdMs > 0 {
		// Chord with hold: keydown all, sleep via separate runner, keyup reverse.
		parts := strings.Split(key, "+")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
			if parts[i] == "" {
				return nil, fmt.Errorf("invalid key chord %q", key)
			}
		}
		var out [][]string
		for _, p := range parts {
			out = append(out, []string{"keydown", "--", p})
		}
		out = append(out, []string{"__sleep_ms__", strconv.Itoa(holdMs)})
		for i := len(parts) - 1; i >= 0; i-- {
			out = append(out, []string{"keyup", "--", parts[i]})
		}
		return out, nil
	}
	return [][]string{{"key", "--", key}}, nil
}

func parseHoldMsFlag(rest []string) (holdMs int, remaining []string, err error) {
	remaining = make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--hold-ms" || rest[i] == "--hold" {
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return 0, nil, errors.New("--hold-ms requires an integer millisecond value")
			}
			holdMs, _ = strconv.Atoi(rest[i])
			if holdMs < 0 {
				return 0, nil, errors.New("--hold-ms must be >= 0")
			}
			continue
		}
		remaining = append(remaining, rest[i])
	}
	return holdMs, remaining, nil
}

func parseModifiersFlag(rest []string) (mods []string, remaining []string, err error) {
	remaining = make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--modifiers" || rest[i] == "--mods" {
			i++
			if i >= len(rest) {
				return nil, nil, errors.New("--modifiers requires a value (e.g. ctrl+shift)")
			}
			mods = splitModifiers(rest[i])
			continue
		}
		remaining = append(remaining, rest[i])
	}
	return mods, remaining, nil
}

func parseOptionalXY(rest []string) (x, y string, remaining []string, err error) {
	remaining = make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--x":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return "", "", nil, errors.New("--x requires an integer")
			}
			x = rest[i]
		case "--y":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return "", "", nil, errors.New("--y requires an integer")
			}
			y = rest[i]
		default:
			remaining = append(remaining, rest[i])
		}
	}
	if (x == "") != (y == "") {
		return "", "", nil, errors.New("--x and --y must be provided together")
	}
	return x, y, remaining, nil
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

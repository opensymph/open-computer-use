package main

// Desktop-level display commands, mirroring the Linux runtime's desktop.go
// one-for-one (same command names, flags, and output shapes) so scripts can
// drive any platform the same way: whole-screen capture, pointer query, global
// SendInput input, and ffmpeg screen recording. These sit alongside the UIA
// tool surface: the 14 MCP tools stay per-window and non-intrusive, while
// these commands operate on the whole desktop like xdotool/ffmpeg do. The
// native I/O lives in desktop_windows.go; this file keeps the parsing and
// command construction portable so the pure-logic tests stay trivial.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pointerInfo mirrors the Linux runtime's cursor-position JSON: pointer
// coordinates plus the whole-desktop (virtual screen) size.
type pointerInfo struct {
	X            int `json:"x"`
	Y            int `json:"y"`
	ScreenWidth  int `json:"screen_width"`
	ScreenHeight int `json:"screen_height"`
}

// recordState is persisted to the record pidfile so a later `record stop` can
// find and signal the detached ffmpeg process.
type recordState struct {
	PID    int    `json:"pid"`
	Output string `json:"output"`
}

// inputOp is one SendInput primitive. buildInputOps produces them from the
// parsed action; runInputOps executes them in order. Kept as data so the
// construction stays pure and unit-testable without a display.
type inputOp struct {
	kind string // move, click, drag, scroll, type, key
	x    int
	y    int
	toX  int
	toY  int
	// button keeps the X11-style numbering shared with the Linux runtime
	// (1 left, 2 middle, 3 right).
	button int
	count  int
	// dy/dx are wheel notches (dy positive scrolls up, dx positive scrolls
	// right), matching the Linux scroll directions.
	dy   int
	dx   int
	text string
	key  string
}

func isIntArg(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

// mouseButtonNumber maps the official button names (and single-letter aliases)
// to the X11-style button numbering shared across the desktop commands.
func mouseButtonNumber(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "left", "l", "1":
		return 1, nil
	case "middle", "m", "2":
		return 2, nil
	case "right", "r", "3":
		return 3, nil
	default:
		return 0, fmt.Errorf("invalid mouse button %q (left, right, or middle)", value)
	}
}

// scrollNotches maps a scroll direction to wheel notches: dy positive scrolls
// up, dx positive scrolls right.
func scrollNotches(direction string) (int, int, bool) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up":
		return 1, 0, true
	case "down":
		return -1, 0, true
	case "left":
		return 0, -1, true
	case "right":
		return 0, 1, true
	default:
		return 0, 0, false
	}
}

// --- screenshot ------------------------------------------------------------

func runScreenshotCommand(args []string, stdout io.Writer) error {
	var output string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--output", "-o":
			i++
			if i >= len(args) {
				return errors.New("--output requires a value")
			}
			output = args[i]
		default:
			return fmt.Errorf("unknown screenshot option: %s", args[i])
		}
	}
	img, err := captureScreenImage()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fmt.Errorf("cannot encode screenshot: %w", err)
	}
	bounds := img.Bounds()
	if output != "" {
		if err := os.WriteFile(output, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("cannot write screenshot: %w", err)
		}
		fmt.Fprintf(stdout, "Saved %dx%d screenshot to %s\n", bounds.Dx(), bounds.Dy(), output)
		return nil
	}
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(buf.Bytes()))
	return nil
}

// --- cursor-position -------------------------------------------------------

func runCursorPositionCommand(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown cursor-position option: %s", args[0])
	}
	info, err := queryPointer()
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

// --- input (SendInput) -----------------------------------------------------

func runInputCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("input requires an action: move, click, drag, scroll, type, key, or wait")
	}
	action := args[0]
	rest := args[1:]

	// wait is a local sleep, not a pointer/keyboard action, so it is ungated.
	if action == "wait" {
		duration, err := parseWaitDuration(rest)
		if err != nil {
			return err
		}
		time.Sleep(duration)
		fmt.Fprintf(stdout, "waited %s\n", duration)
		return nil
	}

	ops, err := buildInputOps(action, rest)
	if err != nil {
		return err
	}
	// Global synthetic input moves the real pointer / keyboard and can change
	// foreground focus, so it sits behind the same explicit gate as
	// input_method=global / click_method=global.
	if !envFlagEnabled(foregroundInputFlag) {
		return fmt.Errorf("input actions move the real pointer/keyboard and require %s=1", foregroundInputFlag)
	}
	if err := runInputOps(ops); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "input %s ok\n", action)
	return nil
}

// buildInputOps turns a parsed input action into SendInput operations. Kept
// pure so it is unit tested without a display.
func buildInputOps(action string, rest []string) ([]inputOp, error) {
	switch action {
	case "move":
		if len(rest) != 2 || !isIntArg(rest[0]) || !isIntArg(rest[1]) {
			return nil, errors.New("move requires integer <x> <y>")
		}
		x, _ := strconv.Atoi(rest[0])
		y, _ := strconv.Atoi(rest[1])
		return []inputOp{{kind: "move", x: x, y: y}}, nil

	case "click":
		button, count, x, y, err := parseClickParams(rest)
		if err != nil {
			return nil, err
		}
		ops := make([]inputOp, 0, 2)
		if x != "" && y != "" {
			ops = append(ops, inputOp{kind: "move", x: atoiOrZero(x), y: atoiOrZero(y)})
		}
		return append(ops, inputOp{kind: "click", button: button, count: count}), nil

	case "drag":
		if len(rest) < 4 {
			return nil, errors.New("drag requires <from_x> <from_y> <to_x> <to_y>")
		}
		for _, value := range rest[:4] {
			if !isIntArg(value) {
				return nil, errors.New("drag coordinates must be integers")
			}
		}
		button, err := parseButtonFlag(rest[4:], 1)
		if err != nil {
			return nil, err
		}
		return []inputOp{{
			kind:   "drag",
			x:      atoiOrZero(rest[0]),
			y:      atoiOrZero(rest[1]),
			toX:    atoiOrZero(rest[2]),
			toY:    atoiOrZero(rest[3]),
			button: button,
		}}, nil

	case "scroll":
		if len(rest) == 0 {
			return nil, errors.New("scroll requires a direction: up, down, left, or right")
		}
		dy, dx, ok := scrollNotches(rest[0])
		if !ok {
			return nil, fmt.Errorf("invalid scroll direction %q (up, down, left, right)", rest[0])
		}
		amount, err := parseAmountFlag(rest[1:], 3)
		if err != nil {
			return nil, err
		}
		return []inputOp{{kind: "scroll", dy: dy * amount, dx: dx * amount}}, nil

	case "type":
		if len(rest) == 0 {
			return nil, errors.New("type requires text, e.g. 'input type \"hello\"'")
		}
		return []inputOp{{kind: "type", text: strings.Join(rest, " ")}}, nil

	case "key":
		if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
			return nil, errors.New("key requires a single key or chord, e.g. 'input key ctrl+s'")
		}
		// Same non-negotiable deny as press_key: the Windows/Meta key must
		// never be pressed, even on the raw display-level path.
		if err := validateKeyChord(rest[0]); err != nil {
			return nil, err
		}
		return []inputOp{{kind: "key", key: rest[0]}}, nil

	default:
		return nil, fmt.Errorf("unknown input action: %s", action)
	}
}

func atoiOrZero(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func parseWaitDuration(rest []string) (time.Duration, error) {
	if len(rest) != 1 {
		return 0, errors.New("wait requires a duration in seconds, e.g. 'input wait 1.5'")
	}
	seconds, err := strconv.ParseFloat(rest[0], 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("invalid wait duration %q", rest[0])
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func parseClickParams(rest []string) (button, count int, x, y string, err error) {
	button, count = 1, 1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--button", "-b":
			i++
			if i >= len(rest) {
				return 0, 0, "", "", errors.New("--button requires a value")
			}
			button, err = mouseButtonNumber(rest[i])
			if err != nil {
				return 0, 0, "", "", err
			}
		case "--count", "-c":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return 0, 0, "", "", errors.New("--count requires an integer")
			}
			count, _ = strconv.Atoi(rest[i])
			if count < 1 {
				count = 1
			}
		case "--x":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return 0, 0, "", "", errors.New("--x requires an integer")
			}
			x = rest[i]
		case "--y":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return 0, 0, "", "", errors.New("--y requires an integer")
			}
			y = rest[i]
		default:
			return 0, 0, "", "", fmt.Errorf("unknown click option: %s", rest[i])
		}
	}
	if (x == "") != (y == "") {
		return 0, 0, "", "", errors.New("click --x and --y must be provided together")
	}
	return button, count, x, y, nil
}

func parseButtonFlag(rest []string, fallback int) (int, error) {
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--button" || rest[i] == "-b" {
			i++
			if i >= len(rest) {
				return 0, errors.New("--button requires a value")
			}
			return mouseButtonNumber(rest[i])
		}
		return 0, fmt.Errorf("unknown drag option: %s", rest[i])
	}
	return fallback, nil
}

func parseAmountFlag(rest []string, fallback int) (int, error) {
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--amount" || rest[i] == "-n" {
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return 0, errors.New("--amount requires an integer")
			}
			amount, _ := strconv.Atoi(rest[i])
			if amount < 1 {
				amount = 1
			}
			return amount, nil
		}
		return 0, fmt.Errorf("unknown scroll option: %s", rest[i])
	}
	return fallback, nil
}

// --- record (ffmpeg gdigrab) ----------------------------------------------

func runRecordCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("record requires a subcommand: start, stop, or status")
	}
	sub := args[0]
	rest := args[1:]

	var output, pidfile string
	fps := 30
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--output", "-o":
			i++
			if i >= len(rest) {
				return errors.New("--output requires a value")
			}
			output = rest[i]
		case "--pidfile":
			i++
			if i >= len(rest) {
				return errors.New("--pidfile requires a value")
			}
			pidfile = rest[i]
		case "--fps":
			i++
			if i >= len(rest) || !isIntArg(rest[i]) {
				return errors.New("--fps requires an integer")
			}
			fps, _ = strconv.Atoi(rest[i])
		default:
			return fmt.Errorf("unknown record option: %s", rest[i])
		}
	}
	if pidfile == "" {
		pidfile = defaultRecordPidfile()
	}

	switch sub {
	case "start":
		if existing, ok := readRecordPidfile(pidfile); ok && processAlive(existing.PID) {
			return fmt.Errorf("recording already running (pid %d, output %s); run 'record stop' first", existing.PID, existing.Output)
		}
		if output == "" {
			output = defaultRecordOutput(time.Now())
		}
		pid, err := startRecordProcess(output, buildFfmpegRecordArgs(output, fps))
		if err != nil {
			return err
		}
		if err := writeRecordPidfile(pidfile, recordState{PID: pid, Output: output}); err != nil {
			return fmt.Errorf("recording started (pid %d) but pidfile write failed: %w", pid, err)
		}
		fmt.Fprintf(stdout, "recording started: pid=%d fps=%d output=%s\n", pid, fps, output)
		return nil

	case "stop":
		state, ok := readRecordPidfile(pidfile)
		if !ok {
			return errors.New("no recording in progress (pidfile not found)")
		}
		stopErr := stopRecordProcess(state.PID)
		_ = os.Remove(pidfile)
		if stopErr != nil {
			return fmt.Errorf("recording output=%s but stop had a problem: %w", state.Output, stopErr)
		}
		fmt.Fprintf(stdout, "recording stopped: output=%s\n", state.Output)
		return nil

	case "status":
		state, ok := readRecordPidfile(pidfile)
		status := map[string]any{"running": ok && processAlive(state.PID)}
		if ok {
			status["pid"] = state.PID
			status["output"] = state.Output
		}
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil

	default:
		return fmt.Errorf("unknown record subcommand: %s", sub)
	}
}

// buildFfmpegRecordArgs mirrors the Linux recorder shape: gdigrab of the whole
// desktop into an H.264 mp4 with a broadly-compatible pixel format (gdigrab is
// the Windows sibling of x11grab). Kept pure for testing.
func buildFfmpegRecordArgs(output string, fps int) []string {
	if fps <= 0 {
		fps = 30
	}
	return []string{
		"-nostdin",
		"-y",
		"-f", "gdigrab",
		"-framerate", strconv.Itoa(fps),
		"-i", "desktop",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-pix_fmt", "yuv420p",
		output,
	}
}

func defaultRecordPidfile() string {
	return filepath.Join(os.TempDir(), "open-computer-use-record.pid")
}

func defaultRecordOutput(now time.Time) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("open-computer-use-recording-%s.mp4", now.Format("20060102-150405")))
}

func writeRecordPidfile(path string, state recordState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readRecordPidfile(path string) (recordState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return recordState{}, false
	}
	var state recordState
	if json.Unmarshal(data, &state) != nil || state.PID <= 0 {
		return recordState{}, false
	}
	return state, true
}

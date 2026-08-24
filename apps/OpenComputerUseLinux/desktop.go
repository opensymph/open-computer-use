package main

// Desktop-level capabilities that mirror the X11 stack a Cloud Agent style host
// exposes (whole-screen capture, pointer query, global xdotool input, and
// ffmpeg screen recording). These sit alongside the AT-SPI2 tool surface: the
// AT-SPI tools stay non-intrusive and per-app, while these commands operate on
// the whole X11 display like xdotool/ffmpeg do. Observation (screenshot,
// cursor-position) is pure Go over X11; actuation (input) and recording reuse
// the same battle-tested xdotool/ffmpeg binaries the host uses, feature-detected
// at call time. The native I/O lives in desktop_linux.go with !linux stubs so
// the package still compiles and the pure-logic tests run on any host.

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

const defaultRecordPidfile = "/tmp/open-computer-use-record.pid"

// pointerInfo is the result of a QueryPointer against the X11 root window.
type pointerInfo struct {
	X            int `json:"x"`
	Y            int `json:"y"`
	ScreenWidth  int `json:"screen_width"`
	ScreenHeight int `json:"screen_height"`
}

// recordState is persisted to the record pidfile so a later `record stop` can
// find and signal the detached ffmpeg process.
type recordState struct {
	PID     int    `json:"pid"`
	Output  string `json:"output"`
	Display string `json:"display"`
}

// resolveDisplay picks the X11 display target: explicit --display wins, then
// $DISPLAY, then :0. On a VNC/AnyOS desktop the display is usually :1.
func resolveDisplay(flag, envDisplay string) string {
	if trimmed := strings.TrimSpace(flag); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(envDisplay); trimmed != "" {
		return trimmed
	}
	return ":0"
}

func isIntArg(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

// mouseButtonNumber maps the official button names (and single-letter aliases)
// plus raw X button numbers to the X11 button number xdotool expects.
func mouseButtonNumber(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "left", "l", "1":
		return 1, nil
	case "middle", "m", "2":
		return 2, nil
	case "right", "r", "3":
		return 3, nil
	default:
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 1 && n <= 9 {
			return n, nil
		}
		return 0, fmt.Errorf("invalid mouse button %q (left, right, middle, or 1-9)", value)
	}
}

// scrollButton maps a scroll direction to the X11 wheel button number.
func scrollButton(direction string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "up":
		return 4, true
	case "down":
		return 5, true
	case "left":
		return 6, true
	case "right":
		return 7, true
	default:
		return 0, false
	}
}

// --- screenshot ------------------------------------------------------------

func runScreenshotCommand(args []string, stdout io.Writer) error {
	var displayFlag, output string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--display":
			i++
			if i >= len(args) {
				return errors.New("--display requires a value")
			}
			displayFlag = args[i]
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
	display := resolveDisplay(displayFlag, os.Getenv("DISPLAY"))
	img, err := captureScreenImage(display)
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
		fmt.Fprintf(stdout, "Saved %dx%d screenshot of %s to %s\n", bounds.Dx(), bounds.Dy(), display, output)
		return nil
	}
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(buf.Bytes()))
	return nil
}

// --- cursor-position -------------------------------------------------------

func runCursorPositionCommand(args []string, stdout io.Writer) error {
	var displayFlag string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--display":
			i++
			if i >= len(args) {
				return errors.New("--display requires a value")
			}
			displayFlag = args[i]
		default:
			return fmt.Errorf("unknown cursor-position option: %s", args[i])
		}
	}
	display := resolveDisplay(displayFlag, os.Getenv("DISPLAY"))
	info, err := queryPointer(display)
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

// --- input (xdotool) -------------------------------------------------------

func runInputCommand(args []string, stdout io.Writer) error {
	displayFlag, positional, err := extractDisplayFlag(args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("input requires an action: move, click, drag, scroll, type, key, or wait")
	}
	action := positional[0]
	rest := positional[1:]

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

	invocations, err := buildXdotoolInvocations(action, rest)
	if err != nil {
		return err
	}
	// Global synthetic input moves the real pointer / keyboard and can change
	// foreground focus, so it sits behind the same explicit gate as
	// click_method=global.
	if !globalPointerFallbacksEnabled() {
		return errors.New("input actions move the real pointer/keyboard and require OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1")
	}
	display := resolveDisplay(displayFlag, os.Getenv("DISPLAY"))
	if err := runInputInvocations(display, invocations); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "input %s ok on %s\n", action, display)
	return nil
}

// extractDisplayFlag pulls a leading `--display <value>` (if present) out of the
// argument list without disturbing action payloads such as typed text.
func extractDisplayFlag(args []string) (string, []string, error) {
	var display string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--display" {
			i++
			if i >= len(args) {
				return "", nil, errors.New("--display requires a value")
			}
			display = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	return display, rest, nil
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

// buildXdotoolInvocations turns a parsed input action into one or more xdotool
// argument vectors. Kept pure so it is unit tested without a display.
func buildXdotoolInvocations(action string, rest []string) ([][]string, error) {
	switch action {
	case "move":
		if len(rest) != 2 || !isIntArg(rest[0]) || !isIntArg(rest[1]) {
			return nil, errors.New("move requires integer <x> <y>")
		}
		return [][]string{{"mousemove", "--", rest[0], rest[1]}}, nil

	case "click":
		button, count, x, y, err := parseClickParams(rest)
		if err != nil {
			return nil, err
		}
		var invocations [][]string
		if x != "" && y != "" {
			invocations = append(invocations, []string{"mousemove", "--", x, y})
		}
		click := []string{"click"}
		if count > 1 {
			click = append(click, "--repeat", strconv.Itoa(count))
		}
		click = append(click, strconv.Itoa(button))
		return append(invocations, click), nil

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
		btn := strconv.Itoa(button)
		return [][]string{
			{"mousemove", "--", rest[0], rest[1]},
			{"mousedown", btn},
			{"mousemove", "--", rest[2], rest[3]},
			{"mouseup", btn},
		}, nil

	case "scroll":
		if len(rest) == 0 {
			return nil, errors.New("scroll requires a direction: up, down, left, or right")
		}
		button, ok := scrollButton(rest[0])
		if !ok {
			return nil, fmt.Errorf("invalid scroll direction %q (up, down, left, right)", rest[0])
		}
		amount, err := parseAmountFlag(rest[1:], 3)
		if err != nil {
			return nil, err
		}
		return [][]string{{"click", "--repeat", strconv.Itoa(amount), strconv.Itoa(button)}}, nil

	case "type":
		if len(rest) == 0 {
			return nil, errors.New("type requires text, e.g. 'input type \"hello\"'")
		}
		return [][]string{{"type", "--", strings.Join(rest, " ")}}, nil

	case "key":
		if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
			return nil, errors.New("key requires a single key or chord, e.g. 'input key ctrl+s'")
		}
		return [][]string{{"key", "--", rest[0]}}, nil

	default:
		return nil, fmt.Errorf("unknown input action: %s", action)
	}
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

// --- record (ffmpeg x11grab) ----------------------------------------------

func runRecordCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("record requires a subcommand: start, stop, or status")
	}
	sub := args[0]
	rest := args[1:]

	var displayFlag, output, pidfile string
	fps := 30
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--display":
			i++
			if i >= len(rest) {
				return errors.New("--display requires a value")
			}
			displayFlag = rest[i]
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
		pidfile = defaultRecordPidfile
	}

	switch sub {
	case "start":
		if existing, ok := readRecordPidfile(pidfile); ok && processAlive(existing.PID) {
			return fmt.Errorf("recording already running (pid %d, output %s); run 'record stop' first", existing.PID, existing.Output)
		}
		display := resolveDisplay(displayFlag, os.Getenv("DISPLAY"))
		if output == "" {
			output = defaultRecordOutput(time.Now())
		}
		pid, err := startRecordProcess(display, output, buildFfmpegRecordArgs(display, output, fps))
		if err != nil {
			return err
		}
		if err := writeRecordPidfile(pidfile, recordState{PID: pid, Output: output, Display: display}); err != nil {
			return fmt.Errorf("recording started (pid %d) but pidfile write failed: %w", pid, err)
		}
		fmt.Fprintf(stdout, "recording started: pid=%d display=%s fps=%d output=%s\n", pid, display, fps, output)
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
			status["display"] = state.Display
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

// buildFfmpegRecordArgs mirrors the host recorder: x11grab of the display into
// an H.264 mp4 with a broadly-compatible pixel format. Kept pure for testing.
func buildFfmpegRecordArgs(display, output string, fps int) []string {
	if fps <= 0 {
		fps = 30
	}
	return []string{
		"-nostdin",
		"-y",
		"-f", "x11grab",
		"-draw_mouse", "1",
		"-framerate", strconv.Itoa(fps),
		"-i", display,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-pix_fmt", "yuv420p",
		output,
	}
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

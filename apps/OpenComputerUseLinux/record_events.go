package main

// Sidecar input-event log for screen recordings. While a recording is active,
// display-level `input` actions append timeline events so `record polish` can
// rebuild Cursor RecordScreen-style overlays (click ripples, keystroke
// captions, idle speedups, zoom windows, cursor ghost) without depending on
// the proprietary polished-renderer.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const recordEventsVersion = 1

// recordEvent is one timeline action relative to recording start.
type recordEvent struct {
	TMs       int64   `json:"t_ms"`
	Type      string  `json:"type"`
	X         int     `json:"x,omitempty"`
	Y         int     `json:"y,omitempty"`
	ToX       int     `json:"to_x,omitempty"`
	ToY       int     `json:"to_y,omitempty"`
	Button    string  `json:"button,omitempty"`
	Count     int     `json:"count,omitempty"`
	Direction string  `json:"direction,omitempty"`
	Amount    int     `json:"amount,omitempty"`
	Text      string  `json:"text,omitempty"`
	Key       string  `json:"key,omitempty"`
	Seconds   float64 `json:"seconds,omitempty"`
}

type recordEventLog struct {
	Version     int           `json:"version"`
	StartedAtMs int64         `json:"started_at_ms"`
	Width       int           `json:"width,omitempty"`
	Height      int           `json:"height,omitempty"`
	FPS         int           `json:"fps,omitempty"`
	Events      []recordEvent `json:"events"`
}

var recordEventMu sync.Mutex

func recordEventsPath(output string) string {
	ext := filepath.Ext(output)
	if ext == "" {
		return output + ".events.json"
	}
	return strings.TrimSuffix(output, ext) + ".events.json"
}

func initRecordEventLog(output string, width, height, fps int, started time.Time) error {
	log := recordEventLog{
		Version:     recordEventsVersion,
		StartedAtMs: started.UnixMilli(),
		Width:       width,
		Height:      height,
		FPS:         fps,
		Events:      []recordEvent{},
	}
	return writeRecordEventLog(recordEventsPath(output), log)
}

func writeRecordEventLog(path string, log recordEventLog) error {
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readRecordEventLog(path string) (recordEventLog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return recordEventLog{}, err
	}
	var log recordEventLog
	if err := json.Unmarshal(data, &log); err != nil {
		return recordEventLog{}, err
	}
	if log.Events == nil {
		log.Events = []recordEvent{}
	}
	return log, nil
}

// appendRecordEventIfRecording appends an event to the active recording's
// sidecar when a pidfile points at a live recorder. Failures are ignored so
// event logging never breaks input actions.
func appendRecordEventIfRecording(pidfile string, event recordEvent) {
	recordEventMu.Lock()
	defer recordEventMu.Unlock()

	if pidfile == "" {
		pidfile = defaultRecordPidfile
	}
	state, ok := readRecordPidfile(pidfile)
	if !ok || !processAlive(state.PID) || state.Output == "" {
		return
	}
	path := recordEventsPath(state.Output)
	log, err := readRecordEventLog(path)
	if err != nil {
		startedMs := time.Now().UnixMilli()
		if state.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, state.StartedAt); err == nil {
				startedMs = t.UnixMilli()
			}
		}
		log = recordEventLog{Version: recordEventsVersion, StartedAtMs: startedMs, FPS: state.FPS, Events: nil}
	}
	if event.TMs == 0 {
		event.TMs = time.Now().UnixMilli() - log.StartedAtMs
		if event.TMs < 0 {
			event.TMs = 0
		}
	}
	log.Events = append(log.Events, event)
	_ = writeRecordEventLog(path, log)
}

// buildRecordEventFromInput turns a successful input action into a timeline
// event. Kept pure for unit tests.
func buildRecordEventFromInput(action string, rest []string) (recordEvent, bool) {
	switch action {
	case "move":
		if len(rest) != 2 || !isIntArg(rest[0]) || !isIntArg(rest[1]) {
			return recordEvent{}, false
		}
		x, _ := strconv.Atoi(rest[0])
		y, _ := strconv.Atoi(rest[1])
		return recordEvent{Type: "move", X: x, Y: y}, true
	case "click":
		button, count, x, y, err := parseClickParams(rest)
		if err != nil {
			return recordEvent{}, false
		}
		ev := recordEvent{Type: "click", Button: buttonNameFromNumber(button), Count: count}
		if x != "" && y != "" {
			ev.X, _ = strconv.Atoi(x)
			ev.Y, _ = strconv.Atoi(y)
		}
		return ev, true
	case "drag":
		if len(rest) < 4 || !isIntArg(rest[0]) || !isIntArg(rest[1]) || !isIntArg(rest[2]) || !isIntArg(rest[3]) {
			return recordEvent{}, false
		}
		fx, _ := strconv.Atoi(rest[0])
		fy, _ := strconv.Atoi(rest[1])
		tx, _ := strconv.Atoi(rest[2])
		ty, _ := strconv.Atoi(rest[3])
		button := "left"
		if len(rest) >= 6 && (rest[4] == "--button" || rest[4] == "-b") {
			if n, err := mouseButtonNumber(rest[5]); err == nil {
				button = buttonNameFromNumber(n)
			}
		}
		return recordEvent{Type: "drag", X: fx, Y: fy, ToX: tx, ToY: ty, Button: button}, true
	case "scroll":
		if len(rest) == 0 {
			return recordEvent{}, false
		}
		amount := 3
		if len(rest) >= 3 && (rest[1] == "--amount" || rest[1] == "-n") && isIntArg(rest[2]) {
			amount, _ = strconv.Atoi(rest[2])
		}
		return recordEvent{Type: "scroll", Direction: strings.ToLower(rest[0]), Amount: amount}, true
	case "type":
		if len(rest) == 0 {
			return recordEvent{}, false
		}
		return recordEvent{Type: "type", Text: strings.Join(rest, " ")}, true
	case "key":
		if len(rest) != 1 {
			return recordEvent{}, false
		}
		return recordEvent{Type: "key", Key: rest[0]}, true
	case "wait":
		if len(rest) != 1 {
			return recordEvent{}, false
		}
		seconds, err := strconv.ParseFloat(rest[0], 64)
		if err != nil {
			return recordEvent{}, false
		}
		return recordEvent{Type: "wait", Seconds: seconds}, true
	default:
		return recordEvent{}, false
	}
}

func buttonNameFromNumber(n int) string {
	switch n {
	case 2:
		return "middle"
	case 3:
		return "right"
	default:
		return "left"
	}
}

func removeRecordSidecars(output string) {
	_ = os.Remove(recordEventsPath(output))
	_ = os.Remove(output + ".log")
	_ = os.Remove(output + ".ass")
	_ = os.Remove(output + ".polish.log")
}

func relocateFileBestEffort(src, dst string) error {
	if src == dst {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

func defaultPolishedOutput(raw string) string {
	ext := filepath.Ext(raw)
	if ext == "" {
		return raw + ".polished.mp4"
	}
	return strings.TrimSuffix(raw, ext) + ".polished" + ext
}

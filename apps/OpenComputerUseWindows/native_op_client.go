//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The op-subprocess boundary. Heap-corruption soak testing (history
// 2026-08-22/23) proved that on this DWM/driver stack, reading a window with
// ANY native mechanism while it is still settling after a move — a UIA tree
// walk just as much as a WGC/PrintWindow capture — can asynchronously stomp
// the calling process's Go heap (Marshal starts reporting NUL bytes, reflect
// panics), and once it happens the process is dead state. Isolating only the
// capture chain was therefore not enough: the long-lived MCP/CLI process
// still corrupted. This backend moves the ENTIRE native execution surface
// (Win32 + UIA COM + capture) into a sacrificial short-lived child
// ("opencomputerusewindows.exe op"): the parent marshals a psRequest, the
// child runs nativeBackendImpl.call and prints one psResponse JSON line, the
// parent unmarshals and re-marshals it. A child that crashes or emits
// corrupted output costs one retry, never the session.
//
// Fallbacks, in order: `go test` binaries (they do not understand the op
// subcommand), an unresolvable executable, and three consecutive dead
// workers (the window may still be mid-drag) all fall back to the in-process
// implementation — exactly the pre-isolation behavior, with zero new error
// strings: domain failures always ride in psResponse.Error, byte-identical
// with the retired PS-era runtime.
type opSubprocessBackend struct {
	inner *nativeBackendImpl
}

const (
	opWorkerAttempts  = 3
	opWorkerRetryWait = 600 // ms; a moved window needs ~1.5s to settle, so
	// three attempts span enough wall time that the retry observes a stable
	// window (soak E3: tree walks after settling never corrupt).
	opWorkerTimeout = 60 * time.Second
)

func (b *opSubprocessBackend) call(req psRequest) (*psResponse, error) {
	if runningUnderGoTest() {
		return b.inner.call(req)
	}
	self, err := os.Executable()
	if err != nil {
		return b.inner.call(req)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return b.inner.call(req)
	}
	for attempt := 1; attempt <= opWorkerAttempts; attempt++ {
		if attempt > 1 {
			sleepMs(opWorkerRetryWait)
		}
		resp, err := runOpWorker(self, payload)
		if err == nil {
			return resp, nil
		}
	}
	// Every worker died (sustained window churn, or a genuinely broken
	// child). One last in-process attempt preserves the pre-isolation
	// behavior instead of inventing a new error envelope.
	return b.inner.call(req)
}

// runOpWorker executes one op child and validates its output. Corruption in
// the child surfaces as a crash, truncated output, or NUL bytes — all mapped
// to a retryable error.
func runOpWorker(self string, payload []byte) (*psResponse, error) {
	cmd := exec.Command(self, "op")
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(opWorkerTimeout):
		_ = cmd.Process.Kill()
		<-done
		return nil, errors.New("op worker timed out")
	}
	output := bytes.TrimSpace(stdout.Bytes())
	if len(output) == 0 {
		return nil, errors.New("op worker produced no output")
	}
	// A raw NUL byte means the child's encoder buffer itself was stomped;
	// valid JSON can never contain one.
	if bytes.IndexByte(output, 0) >= 0 {
		return nil, errors.New("op worker output contains NUL bytes")
	}
	var resp psResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("op worker output unparseable: %v", err)
	}
	if !psResponseIsClean(&resp) {
		return nil, errors.New("op worker response carries NUL bytes")
	}
	return &resp, nil
}

// psResponseIsClean rejects strings the child managed to marshal (NULs get
// escaped as \u0000, which is valid JSON) — re-marshalled here, such bytes
// would flow into every later response of the long-lived session.
func psResponseIsClean(resp *psResponse) bool {
	if strings.ContainsRune(resp.Text, 0) || strings.ContainsRune(resp.Error, 0) {
		return false
	}
	if snapshot := resp.Snapshot; snapshot != nil {
		if strings.ContainsRune(snapshot.WindowTitle, 0) ||
			strings.ContainsRune(snapshot.FocusedSummary, 0) ||
			strings.ContainsRune(snapshot.SelectedText, 0) {
			return false
		}
		for _, line := range snapshot.TreeLines {
			if strings.ContainsRune(line, 0) {
				return false
			}
		}
	}
	if window := resp.Window; window != nil && strings.ContainsRune(window.Title, 0) {
		return false
	}
	for _, window := range resp.Windows {
		if strings.ContainsRune(window.Title, 0) {
			return false
		}
	}
	return true
}

// testBinaryMode reports whether this process is a `go test` binary judged
// from os.Args[0] alone. Unlike runningUnderGoTest this is safe to call from
// package init(): the test flags are not registered yet at init time, but the
// binary name is already final (go test compiles to *.test / *.test.exe).
func testBinaryMode() bool {
	return strings.HasSuffix(os.Args[0], ".test") || strings.HasSuffix(os.Args[0], ".test.exe")
}

func init() {
	if testBinaryMode() {
		nativeRuntime = &nativeBackendImpl{}
		return
	}
	nativeRuntime = &opSubprocessBackend{inner: &nativeBackendImpl{}}
}

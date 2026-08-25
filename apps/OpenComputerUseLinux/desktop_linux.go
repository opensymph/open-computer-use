//go:build linux

package main

// Native X11 / xdotool / ffmpeg backends for the desktop-level commands. These
// are the only OS-specific pieces; desktop.go keeps the parsing and command
// construction portable so the pure-logic tests run on any host.

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// captureScreenImage grabs the whole X11 root window (all monitors of the
// display) as an RGBA image, reusing the same ZPixmap decoder as the per-window
// AT-SPI capture path.
func captureScreenImage(display string) (image.Image, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to X display %q: %w", display, err)
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	if setup == nil || conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		return nil, errors.New("X server returned no screens")
	}
	screen := setup.Roots[conn.DefaultScreen]
	width, height := int(screen.WidthInPixels), int(screen.HeightInPixels)
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return nil, fmt.Errorf("unexpected root window size %dx%d", width, height)
	}

	reply, err := xproto.GetImage(
		conn, xproto.ImageFormatZPixmap, xproto.Drawable(screen.Root),
		0, 0, uint16(width), uint16(height), ^uint32(0),
	).Reply()
	if err != nil || reply == nil {
		return nil, fmt.Errorf("X11 GetImage failed: %w", err)
	}

	img := decodeZPixmap(setup, &screen, reply, width, height)
	if img == nil {
		return nil, errors.New("unsupported X11 pixel format for screenshot")
	}
	return img, nil
}

// queryPointer returns the pointer position relative to the root window and the
// root window size.
func queryPointer(display string) (pointerInfo, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return pointerInfo{}, fmt.Errorf("cannot connect to X display %q: %w", display, err)
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	if setup == nil || conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		return pointerInfo{}, errors.New("X server returned no screens")
	}
	screen := setup.Roots[conn.DefaultScreen]
	reply, err := xproto.QueryPointer(conn, screen.Root).Reply()
	if err != nil || reply == nil {
		return pointerInfo{}, fmt.Errorf("X11 QueryPointer failed: %w", err)
	}
	return pointerInfo{
		X:            int(reply.RootX),
		Y:            int(reply.RootY),
		ScreenWidth:  int(screen.WidthInPixels),
		ScreenHeight: int(screen.HeightInPixels),
	}, nil
}

// runInputInvocations runs the prepared xdotool argument vectors in order
// against the target display. xdotool is feature-detected; a missing binary is
// reported clearly rather than silently succeeding.
func runInputInvocations(display string, invocations [][]string) error {
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		return errors.New("xdotool is required for input actions but was not found on PATH")
	}
	for _, argv := range invocations {
		if len(argv) > 0 && argv[0] == "__sleep_ms__" {
			ms := 0
			if len(argv) > 1 {
				fmt.Sscanf(argv[1], "%d", &ms)
			}
			if ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			continue
		}
		cmd := exec.Command(xdotool, argv...)
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("xdotool %s failed: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// startRecordProcess launches ffmpeg detached so recording survives this CLI
// process exiting; `record stop` later signals it by pid. stdout/stderr go to a
// sibling .log file for debugging.
func startRecordProcess(display, output string, args []string) (int, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return 0, errors.New("ffmpeg is required for screen recording but was not found on PATH")
	}
	logFile, err := os.Create(output + ".log")
	if err != nil {
		return 0, fmt.Errorf("cannot create recording log: %w", err)
	}
	cmd := exec.Command(ffmpeg, args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// New process group so the recorder is not tied to this CLI's session.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("cannot start ffmpeg: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	logFile.Close()
	return pid, nil
}

// stopRecordProcess asks ffmpeg to finish (SIGINT flushes the moov atom so the
// mp4 is playable) and waits briefly for it to exit.
func stopRecordProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid recording pid")
	}
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return fmt.Errorf("cannot signal ffmpeg pid %d: %w", pid, err)
	}
	for i := 0; i < 100; i++ {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("ffmpeg pid %d did not exit after stop signal", pid)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

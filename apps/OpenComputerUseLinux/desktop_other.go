//go:build !linux

package main

// Non-Linux stubs so the package compiles and the pure-logic tests run on any
// host. The desktop commands are Linux/X11 only; off Linux they report that
// clearly instead of pretending to work.

import (
	"errors"
	"image"
)

var errDesktopUnsupported = errors.New("desktop capabilities are only supported on the Linux X11 runtime")

func captureScreenImage(string) (image.Image, error) { return nil, errDesktopUnsupported }

func queryPointer(string) (pointerInfo, error) { return pointerInfo{}, errDesktopUnsupported }

func runInputInvocations(string, [][]string) error { return errDesktopUnsupported }

func startRecordProcess(string, string, []string) (int, error) { return 0, errDesktopUnsupported }

func stopRecordProcess(int) error { return errDesktopUnsupported }

func processAlive(int) bool { return false }

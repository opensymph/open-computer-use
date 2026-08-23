//go:build linux

package main

import (
	"context"
	"os"
	"strings"
	"time"
)

// nativeLinuxBackend replaces the retired python3 subprocess bridge: the whole
// operation runs in-process over AT-SPI2/D-Bus. Domain failures ride the
// linuxResponse error field, byte-identical with the Python bridge.
type nativeLinuxBackend struct{}

func init() {
	linuxNativeBackend = nativeLinuxBackend{}
}

func (nativeLinuxBackend) performOperation(ctx context.Context, request linuxRequest) (*linuxResponse, error) {
	// require_desktop_session: checked against the merged environment (the
	// Python subprocess saw exactly this), with Python truthiness on values.
	env := envSliceToMap(linuxRuntimeEnvironment(os.Environ()))
	missing := []string{}
	if env["XDG_RUNTIME_DIR"] == "" {
		missing = append(missing, "XDG_RUNTIME_DIR")
	}
	if env["DBUS_SESSION_BUS_ADDRESS"] == "" {
		missing = append(missing, "DBUS_SESSION_BUS_ADDRESS")
	}
	if len(missing) > 0 {
		return &linuxResponse{Error: "Linux runtime requires an active desktop session; missing " + strings.Join(missing, ", ")}, nil
	}

	conn, err := connectATSPI(ctx, env)
	if err != nil {
		return &linuxResponse{Error: "Linux runtime could not connect to AT-SPI2: " + err.Error()}, nil
	}
	defer conn.Close()

	display := env["DISPLAY"]
	rt := &atspiRuntime{
		desktop: &dbNode{
			conn: conn,
			ref:  accessibleRef{Bus: atspiDBusNameRegistry, Path: atspiDBusPathRoot},
		},
		capture:    func(bounds *frame) string { return captureWindowPNG(display, bounds) },
		mouseEvent: conn.mouseEvent,
		keyEvent:   conn.keyEvent,
		sleep:      time.Sleep,
	}
	return performOperation(rt, &request), nil
}

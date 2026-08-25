package main

// CoordinateScaler maps model/API-space coordinates to display pixels
// (clean-room of Cursor ComputerUse CoordinateScaler).

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultAPIWidth  = 1280
	defaultAPIHeight = 800
	envAPIWidth      = "OPEN_COMPUTER_USE_API_WIDTH"
	envAPIHeight     = "OPEN_COMPUTER_USE_API_HEIGHT"
	envAPISize       = "OPEN_COMPUTER_USE_API_SIZE" // WxH
)

type coordScaler struct {
	APIWidth      int
	APIHeight     int
	DisplayWidth  int
	DisplayHeight int
}

func (s coordScaler) active() bool {
	return s.APIWidth > 0 && s.APIHeight > 0 &&
		s.DisplayWidth > 0 && s.DisplayHeight > 0 &&
		(s.APIWidth != s.DisplayWidth || s.APIHeight != s.DisplayHeight)
}

func (s coordScaler) scaleX(x int) int {
	if !s.active() || s.APIWidth == 0 {
		return x
	}
	return int(float64(x)*float64(s.DisplayWidth)/float64(s.APIWidth) + 0.5)
}

func (s coordScaler) scaleY(y int) int {
	if !s.active() || s.APIHeight == 0 {
		return y
	}
	return int(float64(y)*float64(s.DisplayHeight)/float64(s.APIHeight) + 0.5)
}

func (s coordScaler) scaleXY(x, y int) (int, int) {
	return s.scaleX(x), s.scaleY(y)
}

func (s coordScaler) unscaleX(x int) int {
	if !s.active() || s.DisplayWidth == 0 {
		return x
	}
	return int(float64(x)*float64(s.APIWidth)/float64(s.DisplayWidth) + 0.5)
}

func (s coordScaler) unscaleY(y int) int {
	if !s.active() || s.DisplayHeight == 0 {
		return y
	}
	return int(float64(y)*float64(s.APIHeight)/float64(s.DisplayHeight) + 0.5)
}

func parseAPISize(value string) (w, h int, err error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parts := strings.Split(value, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid api size %q (want WxH, e.g. 1280x800)", value)
	}
	w, err = strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("invalid api width in %q", value)
	}
	h, err = strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("invalid api height in %q", value)
	}
	return w, h, nil
}

// resolveAPISizeFromEnv returns API canvas from env, or 0,0 if unset.
func resolveAPISizeFromEnv() (w, h int, err error) {
	if v := strings.TrimSpace(os.Getenv(envAPISize)); v != "" {
		return parseAPISize(v)
	}
	ws := strings.TrimSpace(os.Getenv(envAPIWidth))
	hs := strings.TrimSpace(os.Getenv(envAPIHeight))
	if ws == "" && hs == "" {
		return 0, 0, nil
	}
	if ws == "" || hs == "" {
		return 0, 0, fmt.Errorf("%s and %s must both be set", envAPIWidth, envAPIHeight)
	}
	w, err = strconv.Atoi(ws)
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("invalid %s", envAPIWidth)
	}
	h, err = strconv.Atoi(hs)
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("invalid %s", envAPIHeight)
	}
	return w, h, nil
}

func newCoordScaler(apiW, apiH, dispW, dispH int) coordScaler {
	return coordScaler{
		APIWidth: apiW, APIHeight: apiH,
		DisplayWidth: dispW, DisplayHeight: dispH,
	}
}

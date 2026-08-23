//go:build linux

package main

import (
	"os"
	"syscall"
)

// pathOwnedByUIDStat reports whether the statted path is owned by uid. On
// Linux the owner comes from the stat syscall payload (moved verbatim from
// main.go so the rest of the module compiles on non-Linux hosts for tests).
func pathOwnedByUIDStat(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == uid
}

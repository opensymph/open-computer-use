//go:build !linux

package main

import "os"

// pathOwnedByUIDStat has no portable owner lookup off Linux; the callers only
// use it to validate the runtime dir / proc entries, which do not exist on
// non-Linux hosts anyway (the module is a Linux runtime; this stub exists so
// the pure-logic tests compile and run on any host).
func pathOwnedByUIDStat(info os.FileInfo, uid int) bool {
	return false
}

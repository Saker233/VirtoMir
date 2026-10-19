//go:build unix

package update

import (
	"os"
	"syscall"
)

// execSelf replaces the running process with the binary at exe, keeping arguments and environment.
func execSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}

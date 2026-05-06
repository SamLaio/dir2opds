//go:build linux

package service

import (
	"log/slog"
	"os"
	"syscall"
)

const posixFadvDontNeed = 4

func fadviseDontNeed(f *os.File) {
	if f == nil {
		return
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, posixFadvDontNeed, 0, 0)
	if errno != 0 {
		slog.Debug("posix_fadvise DONTNEED failed", "path", f.Name(), "error", errno)
	}
}

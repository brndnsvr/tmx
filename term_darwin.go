package main

import (
	"syscall"
	"unsafe"
)

// isTerminal asks the tty driver, because /dev/null is a character device
// too and must count as "not a terminal" (fzf would then wait on /dev/tty).
func isTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

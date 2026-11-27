package main

import (
	"syscall"
	"unsafe"
)

// isTerminal asks the tty driver, because /dev/null is a character device
// too and must count as "not a terminal" (fzf would then wait on /dev/tty).
// Linux names the get-attributes ioctl TCGETS where macOS says TIOCGETA.
func isTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

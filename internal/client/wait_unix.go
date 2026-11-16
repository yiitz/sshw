//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package client

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// waitEnterOrTimeout waits until d has elapsed or the user presses Enter on
// the terminal, whichever comes first.
func waitEnterOrTimeout(d time.Duration) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		time.Sleep(d)
		return
	}

	deadline := time.Now().Add(d)
	pollFd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	buf := make([]byte, 256)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		timeout := int(remaining.Milliseconds())
		if timeout < 1 {
			timeout = 1
		}
		n, err := unix.Poll(pollFd, timeout)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			time.Sleep(remaining)
			return
		}
		if n == 0 {
			return
		}
		// Input is only readable once Enter has been pressed. Drain it so it
		// is not replayed to a later interactive session, then reconnect.
		for {
			m, readErr := unix.Read(fd, buf)
			if readErr != nil || m < len(buf) {
				break
			}
		}
		return
	}
}

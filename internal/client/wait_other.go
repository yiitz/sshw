//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package client

import "time"

// waitEnterOrTimeout waits for d. Skipping the wait with Enter is not
// supported on this platform.
func waitEnterOrTimeout(d time.Duration) {
	time.Sleep(d)
}

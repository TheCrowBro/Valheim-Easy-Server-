//go:build !windows

package manager

import (
	"errors"
	"syscall"
)

const isWindows = false

// connReset says err is the other end resetting a connection.
func connReset(err error) bool { return errors.Is(err, syscall.ECONNRESET) }

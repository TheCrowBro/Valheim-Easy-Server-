package manager

import (
	"errors"
	"syscall"
)

const isWindows = true

// connReset says err is the other end resetting a connection. Windows
// reports it as WSAECONNRESET (10054), which isn't the syscall.ECONNRESET
// that Go makes up for Windows.
func connReset(err error) bool {
	return errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.ECONNRESET)
}

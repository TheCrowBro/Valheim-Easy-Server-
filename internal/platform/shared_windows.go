//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

// OpenShared opens a file for reading in a way that lets another program
// rename or delete it while it is open, so following a game's own log
// never stops the game rotating it. Windows refuses to rename or delete a
// file that someone has open unless they opened it with FILE_SHARE_DELETE,
// which os.Open doesn't use; the game must also go on writing to it, hence
// FILE_SHARE_WRITE.
func OpenShared(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		// The error is a syscall.Errno, so errors.Is(err, fs.ErrNotExist)
		// works as it does for os.Open.
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

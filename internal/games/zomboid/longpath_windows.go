//go:build windows

package zomboid

import "syscall"

// longPath is path with every short "8.3" name in it (RUNNER~1) spelt out
// in full (runneradmin), as Windows' GetLongPathName gives it, or path as
// it is when Windows can't say (a folder that doesn't exist yet).
//
// The server's Java works with the full names of its folders: it prints
// them, and it compares paths it builds itself with the ones it is given.
// The app's own data folder is always spelt in full (it comes from
// %LOCALAPPDATA%), but a folder under %TEMP% can hold a short name (on
// GitHub's Windows runners TEMP is C:\Users\RUNNER~1\AppData\Local\Temp),
// and the first real-server check, which ran from such a folder, never got
// the server started (FACTS.md). Giving the server full names costs
// nothing and makes every computer look the same to it.
func longPath(path string) string {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, 300)
	for {
		n, err := syscall.GetLongPathName(p, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return path
		}
		// A big enough buffer gets the name and its length without the
		// final NUL; a short one gets the size it needs, NUL included.
		if n < uint32(len(buf)) {
			return syscall.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n)
	}
}

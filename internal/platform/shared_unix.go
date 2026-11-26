//go:build !windows

package platform

import "os"

// OpenShared opens a file for reading in a way that lets another program
// rename or delete it while it is open, so following a game's own log
// never stops the game rotating it. On macOS and Linux every open file
// already allows that.
func OpenShared(path string) (*os.File, error) {
	return os.Open(path)
}

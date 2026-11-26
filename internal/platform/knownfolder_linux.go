//go:build !windows && !darwin

package platform

import (
	"os"
	"path/filepath"
)

// KnownFolder finds one of the user's folders by name: "appdata",
// "localappdata", "documents", "savedgames", "home", "appsupport".
// It returns "" when there is no such folder on this system. Linux only
// runs the tests: it has a home and a Documents folder, and none of the
// Windows or Mac ones.
func KnownFolder(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	switch name {
	case "home":
		return home
	case "documents":
		return filepath.Join(home, "Documents")
	}
	return ""
}

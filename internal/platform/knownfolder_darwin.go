//go:build darwin

package platform

import (
	"os"
	"path/filepath"
)

// KnownFolder finds one of the user's folders by name: "appdata",
// "localappdata", "documents", "savedgames", "home", "appsupport".
// It returns "" when there is no such folder on this system: the Windows
// folders ("appdata", "localappdata", "savedgames") have no Mac
// equivalent, and a game that keeps its files in "Application Support" on
// a Mac asks for "appsupport".
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
	case "appsupport":
		return filepath.Join(home, "Library", "Application Support")
	}
	return ""
}

//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

// KnownFolder finds one of the user's folders by name: "appdata",
// "localappdata", "documents", "savedgames", "home", "appsupport".
// It returns "" when there is no such folder on this system.
//
// Windows is asked for each one rather than guessing from environment
// variables, because the user (or OneDrive) may have moved Documents
// elsewhere. A folder that doesn't exist yet is still named (Saved Games
// may not, until a game makes it), so the game can create it.
// "appsupport" is a Mac folder and has no Windows equivalent.
func KnownFolder(name string) string {
	var id *windows.KNOWNFOLDERID
	switch name {
	case "appdata":
		id = windows.FOLDERID_RoamingAppData
	case "localappdata":
		id = windows.FOLDERID_LocalAppData
	case "documents":
		id = windows.FOLDERID_Documents
	case "savedgames":
		id = windows.FOLDERID_SavedGames
	case "home":
		id = windows.FOLDERID_Profile
	default:
		return ""
	}
	p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DONT_VERIFY)
	if err != nil || p == "" {
		if name == "home" {
			if h, err := os.UserHomeDir(); err == nil {
				return h
			}
		}
		return ""
	}
	return p
}

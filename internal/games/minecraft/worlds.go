package minecraft

import (
	"os"
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worldstore"
)

// newWorlds is the instance's world library. A Minecraft world is a
// folder holding level.dat, in the folder the server is given as
// --universe (Dirs.Saves). The same folder also holds Minecraft's user
// cache (usercache.json), a file, which the library leaves alone.
//
// Uploads and the owner's own saves are searched with worldstore's own
// rule for folder worlds: the first folder down that holds a file of its
// own is a world, and its subfolders (region, dimensions, players) are
// part of it. Every Minecraft world has level.dat at its top, so this
// finds them. (Spec 5.2's stricter "folders holding level.dat" needs a
// Format.Detect, whose type names internal/worlds, which adapters may not
// import; see the stream's notes.)
func newWorlds(d game.Dirs) game.Worlds {
	return worldstore.New(worldstore.Format{
		Dir:     d.Saves,
		Kind:    worldstore.KindFolder,
		MaxName: 60,
		LocalIn: localSaves,
	}, d.Backups, d.Staging)
}

// localSaves is where the owner's own Minecraft keeps its singleplayer
// worlds: %APPDATA%\.minecraft\saves on Windows, ~/Library/Application
// Support/minecraft/saves on a Mac.
func localSaves(home string) []string {
	switch runtime.GOOS {
	case "windows":
		appdata := platform.KnownFolder("appdata")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{filepath.Join(appdata, ".minecraft", "saves")}
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "minecraft", "saves")}
	}
	return []string{filepath.Join(home, ".minecraft", "saves")}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

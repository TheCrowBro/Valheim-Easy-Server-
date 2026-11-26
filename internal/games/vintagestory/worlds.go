package vintagestory

import (
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worldstore"
)

// A world is one SQLite file, <saves>/Saves/<name>.vcdbs (FACTS.md). While
// the server runs, SQLite keeps the latest changes beside it in
// <name>.vcdbs-wal and an index in <name>.vcdbs-shm, and folds them back
// into the main file when the server stops. All three belong to the world:
// a backup taken while it runs needs the -wal, and a restore must take a
// stale -wal away with the old file, or SQLite would apply it to the
// restored one.
//
// The server finds a world only by its file (serverconfig.json's
// SaveFileLocation); the name inside it is just for show, so a world can be
// renamed by renaming its files. The shared name rules (worldstore) are
// all it needs: any name that is a good file name is a good world name.
const (
	worldExt     = ".vcdbs"
	maxWorldName = 60
)

var worldExts = []string{worldExt, worldExt + "-wal", worldExt + "-shm"}

func newWorldStore(d game.Dirs) game.Worlds {
	return worldstore.New(worldstore.Format{
		Dir: savesDir(d.Saves), Kind: worldstore.KindFiles, Ext: worldExts, MaxName: maxWorldName,
		LocalIn: localWorlds,
	}, d.Backups, d.Staging)
}

// localWorlds is where the owner's own Vintage Story keeps its worlds: the
// Saves folder in VintagestoryData (FACTS.md, from the game's wiki).
func localWorlds(home string) []string {
	var base string
	switch runtime.GOOS {
	case "windows":
		appdata := platform.KnownFolder("appdata")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appdata, "VintagestoryData")
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "VintagestoryData")
	default:
		base = filepath.Join(home, ".config", "VintagestoryData")
	}
	return []string{filepath.Join(base, "Saves")}
}

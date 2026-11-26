package enshrouded

import (
	"path/filepath"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worldstore"
)

// The world layout (FACTS.md, "Worlds"). The server keeps its world in the
// folder its settings file names as saveDirectory: one world per folder,
// in files whose names and format are Keen's to change (1.0 changed them).
// So each of the app's worlds is a folder of its own, saves/<world name>/,
// given to the server as its saveDirectory, and the shared world store
// handles it whole: backups and restores take everything in it, renames
// are folder renames, and a world never shares a file with another one.
const maxWorldName = 40

// worldStore is the shared store, with the folder of each world.
type worldStore struct {
	game.Worlds
	dir string // Dirs.Saves: one folder per world
}

func newWorldStore(d game.Dirs) worldStore {
	f := worldstore.Format{Dir: d.Saves, Kind: worldstore.KindFolder, MaxName: maxWorldName}
	return worldStore{Worlds: worldstore.New(f, d.Backups, d.Staging), dir: d.Saves}
}

// saveDir is the server's saveDirectory for a world.
func (w worldStore) saveDir(name string) string { return filepath.Join(w.dir, name) }

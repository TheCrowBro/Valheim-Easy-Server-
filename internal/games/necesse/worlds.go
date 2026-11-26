package necesse

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worldstore"
)

// The world layout of Necesse 1.x (FACTS.md): each world is one zip file,
// <data>/saves/worlds/<name>.zip, holding a folder with world.dat,
// worldSettings.cfg and the levels. The server finds a world by its file
// name only (a renamed zip loads and saves normally), and its first
// autosave also leaves a copy of the world beside the others called
// LATEST_BACKUP1.zip, which is not a world of its own.
//
// Uploads and the owner's own worlds are found by the store's own rule for
// "files" worlds: every .zip file is offered as a world. A Detect that
// opens each zip to look for world.dat would be stricter, but it has to
// return internal/worlds' Candidate, a package adapters may not import
// (spec 2.1); see FACTS.md.
const (
	worldExt     = ".zip"
	maxWorldName = 40
	// backupPrefix starts the names of the server's own backup copies.
	backupPrefix = "LATEST_BACKUP"
	// emptyZip is the size of a zip with nothing in it: what the server
	// writes for a new world until its first save.
	emptyZip = 22
)

// worldsDir is where the server keeps its worlds, given -datadir.
func worldsDir(saves string) string { return filepath.Join(saves, "saves", "worlds") }

// worldStore is the store with Necesse's own rules for names added.
type worldStore struct{ game.Worlds }

func newWorldStore(d game.Dirs) worldStore {
	dir := worldsDir(d.Saves)
	f := worldstore.Format{
		Dir: dir, Kind: worldstore.KindFiles, Ext: []string{worldExt}, MaxName: maxWorldName,
		List:    func() ([]worldstore.Live, error) { return listWorlds(dir) },
		Files:   func(name string) ([]string, error) { return []string{filepath.Join(dir, name+worldExt)}, nil },
		LocalIn: localWorlds,
	}
	return worldStore{worldstore.New(f, d.Backups, d.Staging)}
}

// NameProblem adds Necesse's rules to the shared ones: the server's own
// backup names are taken, and a name starting with "-" would be read as an
// option on the server's command line.
func (w worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	switch {
	case isBackupName(name):
		return "Necesse uses names starting with " + backupPrefix + " for its own backups. Choose another name."
	case strings.HasPrefix(name, "-"):
		return "A world name can't start with a dash (-)."
	}
	return ""
}

func isBackupName(name string) bool {
	return len(name) >= len(backupPrefix) && strings.EqualFold(name[:len(backupPrefix)], backupPrefix)
}

// listWorlds lists the world zips, leaving out the server's backups.
func listWorlds(dir string) ([]worldstore.Live, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []worldstore.Live
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, worldExt) || len(n) == len(worldExt) {
			continue
		}
		name := strings.TrimSuffix(n, worldExt)
		if isBackupName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, worldstore.Live{Name: name, Size: info.Size(), Modified: info.ModTime(), NeverSaved: info.Size() <= emptyZip})
	}
	return out, nil
}

// localWorlds is where the owner's own Necesse keeps its worlds: the same
// Necesse folder the server would use without -datadir (FACTS.md).
func localWorlds(home string) []string {
	var base string
	switch runtime.GOOS {
	case "windows":
		appdata := platform.KnownFolder("appdata")
		if appdata == "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appdata, "Necesse")
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "Necesse")
	default:
		base = filepath.Join(home, ".config", "Necesse")
	}
	return []string{filepath.Join(base, "saves", "worlds")}
}

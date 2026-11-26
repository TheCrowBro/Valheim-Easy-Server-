package corekeeper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worldstore"
)

// The world layout (FACTS.md). The server keeps up to 30 worlds in one
// data folder, as <data>/worlds/<slot>.world.gzip, chosen by -world. The
// app gives each of its worlds a data folder of its own instead,
// saves/<world name>/, and always runs slot 0 there:
//
//	saves/<world>/worlds/0.world.gzip          the world (and .pugbackup, the one before)
//	saves/<world>/ServerConfig.json            what the server last started with
//	saves/<world>/Admins.json, PlayerBans.json  the server's lists
//
// So a world is a folder named after it, which the shared world store
// handles whole: backups and restores take everything that belongs to the
// world, renames are folder renames, and a world never shares a file with
// another one. The cost is that the server's own Game ID would differ per
// folder, which is why the app gives one Game ID on every start (plan.go).
const (
	maxWorldName = 40
	worldFile    = "0.world.gzip"
	configFile   = "ServerConfig.json"
)

// worldStore is the store with Core Keeper's own rules for names added.
type worldStore struct {
	game.Worlds
	dir string // Dirs.Saves: one folder per world
}

func newWorldStore(d game.Dirs) worldStore {
	f := worldstore.Format{Dir: d.Saves, Kind: worldstore.KindFolder, MaxName: maxWorldName}
	return worldStore{Worlds: worldstore.New(f, d.Backups, d.Staging), dir: d.Saves}
}

// dataPath is the server's data folder for a world.
func (w worldStore) dataPath(name string) string { return filepath.Join(w.dir, name) }

// Exists says whether the server has made the world yet, not only whether
// its folder is there. The folder is there before the world is made: Plan
// makes it for a new world, and the server makes it too and puts its mod
// loader's files in it before anything else (FACTS.md). If that first
// start fails (the graphics driver, Steam, a crash, or a force stop while
// it hangs), the folder stays. Counting it as a world would make the next
// start a start of an existing world, without the mode and seed the owner
// chose, and the server would quietly make a Normal world with a random
// seed (and a world made Creative, or not, can't be switched later). So a
// world exists once its folder holds a world file (made, or uploaded: the
// ones tidyWorld puts in place count too) or the ServerConfig.json the
// server writes early in every start, which keeps the mode and seed for
// the starts after it.
//
// List still shows such a folder (as not made yet while it is empty), so
// the owner sees the world they asked for and can delete or rename it.
// Every other change (backups, restores, imports) goes through the shared
// store, which counts the folder, as before.
func (w worldStore) Exists(name string) bool {
	return w.Worlds.Exists(name) && made(w.dataPath(name))
}

// made says whether the server has made a world in the data folder dir.
func made(dir string) bool {
	return fileExists(filepath.Join(dir, configFile)) || len(worldFiles(dir)) > 0
}

// NameProblem adds Core Keeper's rule to the shared ones: the world's name
// is a folder on the server's command line, where the server reads a
// value starting with a dash as another option.
func (w worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	if strings.HasPrefix(name, "-") {
		return "A world name can't start with a dash (-)."
	}
	return ""
}

// reWorldFile matches a world file of any slot: what Core Keeper itself
// (the game, or a server's data folder) calls a world.
var reWorldFile = regexp.MustCompile(`^([0-9]|[12][0-9])\.world\.gzip$`)

// tidyWorld lays out a world folder that came from an upload the way the
// server needs it: when the folder has no worlds/0.world.gzip but holds
// exactly one world file of another slot, at its top or in worlds/ (a
// world copied from the owner's own game, where worlds are numbered by
// slot), that file becomes slot 0. It says which file it moved, if any.
//
// It never guesses between several: with more than one world file, and
// none in slot 0, the server would quietly make a new world instead, so
// the start stops and says what to do.
func tidyWorld(dir string) (string, error) {
	target := filepath.Join(dir, "worlds", worldFile)
	if _, err := os.Stat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	found := worldFiles(dir)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
	default:
		slices.Sort(found)
		return "", fmt.Errorf("This world's folder holds %d Core Keeper worlds (%s) and the server can run only one. Keep the one you want as worlds/%s in the folder %s, or upload it on its own.",
			len(found), strings.Join(found, ", "), worldFile, dir)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(dir, found[0]), target); err != nil {
		return "", fmt.Errorf("couldn't move the world file %s into place: %w", found[0], err)
	}
	// The game keeps the save before the last one beside it; it belongs to
	// the same world.
	if old := filepath.Join(dir, found[0]+".pugbackup"); fileExists(old) {
		os.Rename(old, target+".pugbackup")
	}
	return found[0], nil
}

// worldFiles lists the world files of any slot in the data folder dir, at
// its top or in worlds/, relative to dir.
func worldFiles(dir string) []string {
	var found []string
	for _, sub := range []string{"", "worlds"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type().IsRegular() && reWorldFile.MatchString(e.Name()) {
				found = append(found, filepath.Join(sub, e.Name()))
			}
		}
	}
	return found
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

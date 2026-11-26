package zomboid

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worldstore"
)

// The world layout of Build 42 under the saves folder (-cachedir,
// FACTS.md). A world is what the server calls a "server name"; its files
// are spread over four places, all named after it:
//
//	Saves/Multiplayer/<world>/        the map, players.db, vehicles.db, …
//	db/<world>.db                     the accounts (usernames, passwords, admins, bans)
//	Server/<world>.ini                its settings
//	Server/<world>_SandboxVars.lua    its sandbox settings, and the two spawn files
//
// A world exists once the server has made its map folder, at its first
// start. Backups, restores and deletes take all of these together, so a
// restored world has its own accounts and settings back.
const maxWorldName = 40

// multiplayerDir is where the worlds' maps are.
func multiplayerDir(saves string) string { return filepath.Join(saves, "Saves", "Multiplayer") }

// worldFiles are the files and folders of one world (worldstore keeps only
// those that exist).
func worldFiles(saves, name string) []string {
	server := filepath.Join(saves, "Server")
	return []string{
		filepath.Join(multiplayerDir(saves), name),
		filepath.Join(saves, "db", name+".db"),
		filepath.Join(server, name+".ini"),
		filepath.Join(server, name+"_SandboxVars.lua"),
		filepath.Join(server, name+"_spawnregions.lua"),
		filepath.Join(server, name+"_spawnpoints.lua"),
	}
}

// worldStore is the store with Project Zomboid's own rules for names
// added.
type worldStore struct{ game.Worlds }

func newWorldStore(d game.Dirs) worldStore {
	saves := d.Saves
	f := worldstore.Format{
		Dir: saves, Kind: worldstore.KindFolder, MaxName: maxWorldName,
		List:  func() ([]worldstore.Live, error) { return listWorlds(saves) },
		Files: func(name string) ([]string, error) { return worldFiles(saves, name), nil },
	}
	return worldStore{worldstore.New(f, d.Backups, d.Staging)}
}

// reWorldName is what a world can be called: plain letters, numbers,
// spaces, dashes and underscores. The name goes on the server's command
// line (-servername), which Java on Windows gets in an older character set
// (FACTS.md), and into four file names.
var reWorldName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]*$`)

// NameProblem adds Project Zomboid's rule to the shared ones.
func (w worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	if !reWorldName.MatchString(name) {
		return "A Project Zomboid world name can only have letters (A to Z), numbers, spaces, dashes (-) and underscores (_)."
	}
	return ""
}

// listWorlds lists the worlds: the folders in Saves/Multiplayer. A world's
// size and last change take in all its files.
func listWorlds(saves string) ([]worldstore.Live, error) {
	entries, err := os.ReadDir(multiplayerDir(saves))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []worldstore.Live
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		l := worldstore.Live{Name: name}
		files := 0
		for _, p := range worldFiles(saves, name) {
			size, mod, n := treeInfo(p)
			l.Size += size
			files += n
			if mod.After(l.Modified) {
				l.Modified = mod
			}
		}
		// A map folder the server hasn't written anything into yet.
		l.NeverSaved = files == 0 || !hasFiles(filepath.Join(multiplayerDir(saves), name))
		out = append(out, l)
	}
	return out, nil
}

// treeInfo adds up the regular files at or under path: their total size,
// the newest change and how many there are.
func treeInfo(path string) (size int64, mod time.Time, files int) {
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			files++
			if info.ModTime().After(mod) {
				mod = info.ModTime()
			}
		}
		return nil
	})
	return size, mod, files
}

func hasFiles(dir string) bool {
	_, _, n := treeInfo(dir)
	return n > 0
}

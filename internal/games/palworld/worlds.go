package palworld

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worldstore"
)

// The world layout of Palworld 1.0 (FACTS.md): every world is a folder in
// Pal/Saved/SaveGames/0/ named with a 32-character id (hexadecimal, as
// "C8A34115A3414E9E8EE21C5D7BC7391E"), holding Level.sav (the world),
// LevelMeta.sav, a Players folder with one file per player, and the
// server's own rolling copies in backup/ (bIsUseBackupSaveData, on by
// default). GameUserSettings.ini's DedicatedServerName says which folder
// the server loads; a name with no folder makes a new world there.
//
// The names the app shows are kept in the instance's State, as
// "world.<id>" = name. A folder with no name there (copied in by hand, or
// made by the server itself) is listed under its id.
//
// The app's backups leave out the server's own backup/ folder: it can hold
// dozens of copies of the world, and the app's backups are what restores
// use. A world counts as there once Level.sav is.
const (
	statePrefix  = "world."
	maxWorldName = 40
	levelFile    = "Level.sav"
	serverBackup = "backup"
	// statePlanned is the id the latest start was given, so the next plan
	// can tell whether the server used it (worldID).
	statePlanned = "planned"
)

// reWorldID is what the server's world ids look like.
var reWorldID = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)

// newWorldID makes an id the way the server writes them: 32 upper-case
// hexadecimal digits. Tests replace it.
var newWorldID = func() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

// worldStore is the shared store with Palworld's own rules on top.
type worldStore struct {
	game.Worlds
	s   *server
	dir string
}

func newWorldStore(s *server) *worldStore {
	dir := s.savesDir()
	w := &worldStore{s: s, dir: dir}
	f := worldstore.Format{
		Dir: dir, Kind: worldstore.KindFolder, MaxName: maxWorldName,
		List:  w.list,
		Files: w.files,
	}
	w.Worlds = worldstore.New(f, s.env.Dirs.Backups, s.env.Dirs.Staging)
	return w
}

// names maps each world id in State to its name (ids in upper case).
func names(st game.Values) map[string]string {
	out := map[string]string{}
	for k, v := range st {
		if id, ok := strings.CutPrefix(k, statePrefix); ok && reWorldID.MatchString(id) && v != "" {
			out[strings.ToUpper(id)] = v
		}
	}
	return out
}

// folders lists the world folders on disk that hold a world, by their id
// as the folder is spelled.
func (w *worldStore) folders() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if st, err := os.Stat(filepath.Join(w.dir, e.Name(), levelFile)); err == nil && st.Mode().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// list is the worlds on disk, under the names the app gave them.
func (w *worldStore) list() ([]worldstore.Live, error) {
	ids, err := w.folders()
	if err != nil {
		return nil, err
	}
	known := names(w.s.store.Get().State)
	slices.Sort(ids)
	used := map[string]bool{}
	var out []worldstore.Live
	for _, id := range ids {
		name := known[strings.ToUpper(id)]
		if name == "" || used[strings.ToLower(name)] {
			name = id
		}
		used[strings.ToLower(name)] = true
		l := worldstore.Live{Name: name}
		l.Size, l.Modified = treeSize(w.worldFiles(id))
		out = append(out, l)
	}
	return out, nil
}

// idOf finds a world's id from the instance's State.
func (w *worldStore) idOf(name string) (string, bool) {
	return w.idIn(w.s.store.Get().State, name)
}

// idIn finds a world's id: the one st gives the name (one whose folder is
// there first), else the name itself when it is a folder's id.
func (w *worldStore) idIn(st game.Values, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	var ids []string
	for id, n := range names(st) {
		if n == name {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, err := os.Stat(filepath.Join(w.dir, id, levelFile)); err == nil {
			return id, true
		}
	}
	if len(ids) > 0 {
		return ids[0], true
	}
	if reWorldID.MatchString(name) {
		if st, err := os.Stat(filepath.Join(w.dir, name)); err == nil && st.IsDir() {
			return name, true
		}
	}
	return "", false
}

// worldFiles is what makes up a world on disk: everything in its folder
// but the server's own backups.
func (w *worldStore) worldFiles(id string) []string {
	entries, err := os.ReadDir(filepath.Join(w.dir, id))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.EqualFold(e.Name(), serverBackup) {
			continue
		}
		out = append(out, filepath.Join(w.dir, id, e.Name()))
	}
	return out
}

// files is Format.Files: the world's files, for its backups, restores and
// deletes. A world whose folder isn't there (restoring a deleted one) has
// none, and its backup's files go back to the folder they came from.
func (w *worldStore) files(name string) ([]string, error) {
	id, ok := w.idOf(name)
	if !ok {
		return nil, nil
	}
	return w.worldFiles(id), nil
}

// treeSize adds up the files under the paths and finds the newest change.
func treeSize(paths []string) (size int64, mod time.Time) {
	for _, p := range paths {
		filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				size += info.Size()
				if info.ModTime().After(mod) {
					mod = info.ModTime()
				}
			}
			return nil
		})
	}
	return size, mod
}

// NameProblem adds Palworld's own rule to the shared ones: a name that
// looks like one of the server's world ids would be mistaken for one.
func (w *worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	if reWorldID.MatchString(name) {
		return "That name looks like one of Palworld's own world ids. Pick another."
	}
	return ""
}

// Delete removes the world (the shared store keeps a backup first), then
// its folder, which by then holds only the server's own copies of it.
func (w *worldStore) Delete(name string) error {
	id, ok := w.idOf(name)
	if err := w.Worlds.Delete(name); err != nil {
		return err
	}
	if ok {
		dir := filepath.Join(w.dir, id)
		if _, err := os.Stat(filepath.Join(dir, levelFile)); errors.Is(err, fs.ErrNotExist) {
			os.RemoveAll(dir)
		}
	}
	return nil
}

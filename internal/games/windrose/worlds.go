package windrose

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worldstore"
)

// The world layout of the dedicated server since 0.10.0.5 (the bundled
// DedicatedServer.md and SaveWorkflow.md, FACTS.md "Worlds"). Under
// R5/Saved/SaveProfiles/Default:
//
//   - RocksDB_v2/<game version>/Worlds/<world id>/ is the world's live
//     RocksDB database and its WorldDescription.json (its WorldName, its
//     difficulty preset). The server overwrites and deletes files here
//     while it runs.
//   - RocksDB_v2_Backups/<game version>/Worlds/<world id>/ holds the
//     server's own backups of the world, zip files, the newest one ending
//     in "_Latest.zip". It writes one every minute or so while the world
//     changes and one when it stops, and at every start it loads the world
//     from the _Latest one.
//
// A world id is 32 hexadecimal digits; ServerDescription.json's
// WorldIslandId says which world a start loads.
//
// So one world, for the app's backups, restores and deletes, is both of
// its folders: a live database alone would be replaced from the server's
// own backup at the next start, and the backups alone lack the
// WorldDescription.json that names the world. The app backs a world up
// only while the server is stopped, because the live folder is a database
// the running server keeps changing (spec 5.11).
//
// The names the app shows are kept in the instance's State, as
// "world.<id>" = name. A world without one there (the owner's own, made
// before the app) is listed under its WorldName, or its id when that
// name is taken or can't be a file name.
const (
	statePrefix  = "world."
	maxWorldName = 40
	liveRoot     = "RocksDB_v2"
	backupRoot   = "RocksDB_v2_Backups"
	worldsFolder = "Worlds"
	worldFile    = "WorldDescription.json"
	latestSuffix = "_latest.zip" // compared in lower case
	// statePlanned is the id the latest new world was given, so the app
	// can tell whether the server used it (names).
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

// running says whether the server's program is running now (a backup
// waits for it to stop). Tests replace it.
var running = func(program string) bool {
	found, err := platform.FindByExe(program)
	return err == nil && len(found) > 0
}

// worldStore is the shared store with Windrose's own rules on top.
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

// found is one world on disk.
type found struct {
	id     string   // as its folders spell it
	paths  []string // its live and backup folders that are there
	name   string   // WorldDescription.json's WorldName
	latest bool     // the server has its _Latest backup of it
	live   bool     // its live folder has WorldDescription.json
}

// scan finds the worlds on disk, by id in upper case.
func (w *worldStore) scan() (map[string]*found, error) {
	out := map[string]*found{}
	for _, root := range []string{liveRoot, backupRoot} {
		versions, err := os.ReadDir(filepath.Join(w.dir, root))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if !v.IsDir() || strings.HasPrefix(v.Name(), ".") {
				continue
			}
			base := filepath.Join(w.dir, root, v.Name(), worldsFolder)
			entries, err := os.ReadDir(base)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() || !reWorldID.MatchString(e.Name()) {
					continue
				}
				id := strings.ToUpper(e.Name())
				f := out[id]
				if f == nil {
					f = &found{id: e.Name()}
					out[id] = f
				}
				path := filepath.Join(base, e.Name())
				f.paths = append(f.paths, path)
				if root == liveRoot {
					if name, ok := readWorldName(filepath.Join(path, worldFile)); ok {
						f.live = true
						if f.name == "" {
							f.name = name
						}
					}
				} else if hasLatest(path) {
					f.latest = true
				}
			}
		}
	}
	// A folder with neither WorldDescription.json nor a backup is what a
	// world's database leaves behind once it's gone; not a world.
	for id, f := range out {
		if !f.live && !f.latest {
			delete(out, id)
		}
	}
	return out, nil
}

// readWorldName reads WorldDescription.json's WorldName.
func readWorldName(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var v struct {
		WorldDescription struct {
			WorldName string
		}
	}
	if json.Unmarshal(bytes.TrimPrefix(b, []byte(byteOrderMark)), &v) != nil {
		return "", true // a world, whatever its file says
	}
	return v.WorldDescription.WorldName, true
}

// hasLatest says whether a world's backup folder has the server's
// _Latest backup.
func hasLatest(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e fs.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), latestSuffix)
	})
}

// stateNames maps each world id in State to its name (ids in upper case).
func stateNames(st game.Values) map[string]string {
	out := map[string]string{}
	for k, v := range st {
		if id, ok := strings.CutPrefix(k, statePrefix); ok && reWorldID.MatchString(id) && v != "" {
			out[strings.ToUpper(id)] = v
		}
	}
	return out
}

// named is the worlds on disk with the names the app shows, by id.
//
// current is the world ServerDescription.json selects (upper case, "" when
// unknown). When the latest new world's id isn't on disk but the selected
// world is there and has no name, the server made the new world under an
// id of its own and wrote it into the file: it is that new world.
func named(st game.Values, worlds map[string]*found, current string) map[string]string {
	known := stateNames(st)
	if planned := strings.ToUpper(st[statePlanned]); planned != "" && worlds[planned] == nil &&
		current != "" && worlds[current] != nil && known[current] == "" && known[planned] != "" {
		known[current] = known[planned]
	}
	ids := make([]string, 0, len(worlds))
	for id := range worlds {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := map[string]string{}
	used := map[string]bool{}
	take := func(id, name string) bool {
		if name == "" || used[strings.ToLower(name)] {
			return false
		}
		used[strings.ToLower(name)] = true
		out[id] = name
		return true
	}
	// The app's own names first, so a world named by its WorldName never
	// takes a name the owner gave another.
	for _, id := range ids {
		take(id, known[id])
	}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		if n := worlds[id].name; validName(n) && take(id, n) {
			continue
		}
		take(id, worlds[id].id)
	}
	return out
}

// validName says whether a WorldName can be shown as an app world's name:
// the world's backups are kept in a folder of that name.
func validName(n string) bool {
	return n != "" && len([]rune(n)) <= maxWorldName && lineSafe(n) && !strings.ContainsAny(n, `\/:*?"<>|`) &&
		strings.TrimSpace(n) == n && !strings.HasSuffix(n, ".") && !reWorldID.MatchString(n)
}

// current is the world ServerDescription.json selects, in upper case.
func (w *worldStore) current() string {
	d, err := readDescription(w.s.descriptionPath())
	if err != nil {
		return ""
	}
	return strings.ToUpper(d.str(keyWorld))
}

// view is the worlds on disk with their names.
func (w *worldStore) view() (map[string]*found, map[string]string, error) {
	worlds, err := w.scan()
	if err != nil {
		return nil, nil, err
	}
	return worlds, named(w.s.store.Get().State, worlds, w.current()), nil
}

// list is the worlds on disk, under the names the app shows.
func (w *worldStore) list() ([]worldstore.Live, error) {
	worlds, names, err := w.view()
	if err != nil {
		return nil, err
	}
	var out []worldstore.Live
	for id, name := range names {
		l := worldstore.Live{Name: name, NeverSaved: !worlds[id].latest}
		l.Size, l.Modified = treeSize(worlds[id].paths)
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b worldstore.Live) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// idOf finds a world's id on disk by its name.
func (w *worldStore) idOf(name string) (string, bool) {
	worlds, names, err := w.view()
	if err != nil {
		return "", false
	}
	for id, n := range names {
		if n == name {
			return worlds[id].id, true
		}
	}
	return "", false
}

// files is Format.Files: a world's folders, for its backups, restores and
// deletes. A world that isn't on disk (restoring a deleted one) has none,
// and its backup's folders go back where they came from.
//
// The world the server has loaded can't be backed up while it runs (nor
// restored or deleted, which the manager refuses anyway): its live folder
// is a database the server keeps rewriting, so a copy would be a mix of
// moments (spec 5.11). The shared store's Backup, Restore and Delete all
// ask for the files first, so the refusal here covers them all.
func (w *worldStore) files(name string) ([]string, error) {
	worlds, err := w.scan()
	if err != nil {
		return nil, err
	}
	current := w.current()
	for id, n := range named(w.s.store.Get().State, worlds, current) {
		if n != name {
			continue
		}
		if id == current && running(w.s.Program()) {
			return nil, errStopFirst
		}
		return slices.Clone(worlds[id].paths), nil
	}
	return nil, nil
}

// errStopFirst is the refusal to copy the world the server has loaded.
var errStopFirst = errors.New("Stop the Windrose server first: while it runs, its world is a database it keeps changing, so the app backs it up only while it's stopped.")

// NameProblem adds Windrose's own rule to the shared ones: a name that
// looks like one of the server's world ids would be mistaken for one.
func (w *worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	if reWorldID.MatchString(name) {
		return "That name looks like one of Windrose's own world ids. Pick another."
	}
	return ""
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

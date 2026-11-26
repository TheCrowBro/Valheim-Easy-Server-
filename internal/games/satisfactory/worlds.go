package satisfactory

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worldstore"
)

// The world layout of the Satisfactory 1.x dedicated server (FACTS.md): a
// world is a "session", made of the .sav files in SaveGames/server whose
// header names it: the server's rotating autosaves
// (<session>_autosave_0.sav, _1, _2) and saves made on purpose, which the
// app names after the session (<session>.sav). The session name inside the
// header is what counts, because that is how the server itself groups its
// saves and which one it loads at start; a save whose header can't be read
// is grouped by its file name instead.
//
// Worlds can't be renamed: the name is inside every save (ErrCantRename,
// which the shared store gives for a game with its own list).

// maxWorldName keeps a session name short enough for its save file names.
const maxWorldName = 40

// reAutosave is the end of an autosave's file name.
var reAutosave = regexp.MustCompile(`(?i)_autosave_\d+$`)

// worldStore is the shared store with Satisfactory's own rules on top.
type worldStore struct {
	game.Worlds
	s   *server
	dir string
}

func newWorldStore(s *server) *worldStore {
	dir := s.savesDir()
	w := &worldStore{s: s, dir: dir}
	f := worldstore.Format{
		Dir: dir, Kind: worldstore.KindFiles, Ext: []string{".sav"}, MaxName: maxWorldName,
		List:    w.list,
		Files:   w.files,
		LocalIn: w.localIn,
	}
	w.Worlds = worldstore.New(f, s.env.Dirs.Backups, s.env.Dirs.Staging)
	return w
}

// localIn is Format.LocalIn: the folders where the owner's own copy of
// Satisfactory keeps its saves, one per game account (named with the Steam
// or Epic account's id), in the same SaveGames folder as the server's
// (FACTS.md). Only on Windows, where the game runs, and never in fake
// mode, so tests don't look at the real user's saves.
func (w *worldStore) localIn(home string) []string {
	if w.s.env.Fake != nil || runtime.GOOS != "windows" {
		return nil
	}
	root := filepath.Join(localAppData(), "FactoryGame", "Saved", "SaveGames")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && !strings.HasPrefix(n, ".") && !strings.EqualFold(n, "server") && !strings.EqualFold(n, "blueprints") {
			out = append(out, filepath.Join(root, n))
		}
	}
	return out
}

// saveFile is one .sav file in the saves folder.
type saveFile struct {
	path, stem, session string
	size                int64
	modified, savedAt   time.Time
}

// when is the save's own time, or the file's when the header has none.
func (f saveFile) when() time.Time {
	if !f.savedAt.IsZero() {
		return f.savedAt
	}
	return f.modified
}

// saves lists the .sav files and the session each belongs to.
func (w *worldStore) saves() ([]saveFile, error) {
	entries, err := os.ReadDir(w.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []saveFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".sav") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f := saveFile{path: filepath.Join(w.dir, name), stem: strings.TrimSuffix(name, filepath.Ext(name)),
			size: info.Size(), modified: info.ModTime()}
		if h, err := readSaveHeaderFile(f.path); err == nil && strings.TrimSpace(h.SessionName) != "" {
			f.session, f.savedAt = h.SessionName, h.SavedAt
		} else {
			f.session = reAutosave.ReplaceAllString(f.stem, "")
		}
		out = append(out, f)
	}
	return out, nil
}

// sessions groups the saves by session, letter case apart (the server's
// files are named after the session, and Windows doesn't tell case apart).
// Each group's name is spelled as in its newest save.
func (w *worldStore) sessions() (map[string][]saveFile, error) {
	files, err := w.saves()
	if err != nil {
		return nil, err
	}
	groups := map[string][]saveFile{}
	for _, f := range files {
		k := strings.ToLower(f.session)
		groups[k] = append(groups[k], f)
	}
	for k, g := range groups {
		slices.SortFunc(g, func(a, b saveFile) int { return b.when().Compare(a.when()) })
		groups[k] = g
	}
	return groups, nil
}

// list is Format.List: one world per session.
func (w *worldStore) list() ([]worldstore.Live, error) {
	groups, err := w.sessions()
	if err != nil {
		return nil, err
	}
	var out []worldstore.Live
	for _, g := range groups {
		l := worldstore.Live{Name: g[0].session}
		for _, f := range g {
			l.Size += f.size
			if f.modified.After(l.Modified) {
				l.Modified = f.modified
			}
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b worldstore.Live) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

// files is Format.Files: a session's saves, for its backups, restores and
// deletes. A world with no saves left has none, and its backup's files go
// back to where they came from.
func (w *worldStore) files(name string) ([]string, error) {
	groups, err := w.sessions()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range groups[strings.ToLower(name)] {
		out = append(out, f.path)
	}
	return out, nil
}

// newestSave is the file name (without .sav) of the session's newest
// save, which LoadGame takes.
func (w *worldStore) newestSave(session string) (string, bool) {
	groups, err := w.sessions()
	if err != nil {
		return "", false
	}
	g := groups[strings.ToLower(session)]
	if len(g) == 0 {
		return "", false
	}
	return g[0].stem, true
}

// sessionOf is the session a world name stands for: the session itself,
// spelled as its saves spell it, or, for the name of a save file that was
// imported under a name of its own, the session inside it. Otherwise the
// name as given (a world still to be made).
func (w *worldStore) sessionOf(world string) string {
	if world == "" {
		return ""
	}
	files, err := w.saves()
	if err != nil {
		return world
	}
	for _, f := range files {
		if strings.EqualFold(f.session, world) {
			return f.session
		}
	}
	for _, f := range files {
		if strings.EqualFold(f.stem, world) {
			return f.session
		}
	}
	return world
}

// Exists adds to the shared store's answer the name of a save file: an
// import places a save under the name the owner chose, while the world it
// holds is listed under the session inside it. Starting that name loads
// it (setup.go), and the selection moves to the session's name (plan.go).
func (w *worldStore) Exists(name string) bool {
	if w.Worlds.Exists(name) {
		return true
	}
	files, _ := w.saves()
	return slices.ContainsFunc(files, func(f saveFile) bool { return strings.EqualFold(f.stem, name) })
}

// NameProblem adds Satisfactory's own rule to the shared ones: a name that
// looks like one of the server's autosaves would be mistaken for one.
func (w *worldStore) NameProblem(name string) string {
	if p := w.Worlds.NameProblem(name); p != "" {
		return p
	}
	if reAutosave.MatchString(name) {
		return "That name looks like one of Satisfactory's autosaves. Pick another."
	}
	return ""
}

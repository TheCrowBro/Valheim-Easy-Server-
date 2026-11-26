// Package worldstore is the world library of every game except Valheim
// (spec 2.8). It reuses the format-independent ideas of Valheim's
// internal/worlds Library (zip backups with a fingerprint retry while the
// server saves, all-or-nothing replace with set-aside and undo, import from
// a zip or folder with safe paths) in new code, so Valheim's library never
// changes.
//
// The store only ever writes inside three folders: the live worlds folder
// (Format.Dir), the app's backups (one folder per world, holding
// <stamp>_<reason>.zip files) and the staging folder for uploads. The one
// exception is ExportFile, which writes its zip into the folder its caller
// names. While a world is being replaced, restored, renamed or deleted, its
// files wait in a hidden ".egs-…" folder inside Dir: moves inside one folder
// stay on one drive, so they are instant and can be undone.
//
// Make one store per instance and keep it: unlike Valheim's Library, which
// only holds paths, a store remembers what Scan and Local found (Import
// looks the candidates up there) and lets only one change run at a time.
// See New.
package worldstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// The two layouts a Format's Kind can name.
const (
	KindFolder = "folder" // a world is a folder in Dir named after it
	KindFiles  = "files"  // a world is files in Dir sharing its name, one per Ext
)

// Format is how a game keeps its worlds.
//
// "folder": each world is a folder in Dir, named after the world.
// "files": each world is the files in Dir called <name><ext>, one for each
// extension in Ext ("Forest.wld", "Forest.wld.bak"); the file with the
// first extension must be there for the world to exist.
//
// The optional funcs are for games whose worlds don't follow Kind alone:
//
//   - List lists the worlds instead of looking in Dir. Each name must still
//     be usable as a file name, because the world's backups are kept in a
//     folder of that name.
//   - Files names the files and folders that make up one world, absolute or
//     relative to Dir. They must be inside Dir. Backups, restores and
//     deletes then work on exactly those paths, kept in the backup zip at
//     their place relative to Dir. A world from List or Files can't be
//     renamed here (Rename returns game.ErrCantRename): only the game knows
//     what a new name means for it.
//   - Detect finds worlds in an upload, or in a LocalIn folder, when Kind
//     can't tell a world on its own (Minecraft: a folder holding level.dat).
//     It returns one candidate per world, with ID set to where the world is,
//     relative to root with forward slashes: the world's folder ("." for
//     root itself) for "folder", or its file with one of Ext for "files".
//     The store then gives the candidate its own ID.
//   - LocalIn names the folders where the owner's own copy of the game keeps
//     its worlds on this computer, given the home folder.
//   - Place runs after an imported world has been copied into Dir under its
//     new name, laid out as Kind says, for a game that must change
//     something in it (the name inside the save, a slot number). If it
//     fails, it must leave the copied files where they were, so the import
//     can be undone.
type Format struct {
	Dir     string   // where the live worlds are
	Kind    string   // "folder": a world is a folder; "files": files sharing a base name
	Ext     []string // "files": the extensions of one world, first one names it (".wld", ".wld.bak")
	MaxName int
	// Optional: a game whose worlds don't follow Kind (Palworld GUID folders, Satisfactory sessions).
	List    func() ([]Live, error)
	Files   func(name string) ([]string, error)
	Detect  func(root string) []worlds.Candidate        // in an upload
	LocalIn func(home string) []string                  // the owner's own game's save folders on this OS
	Place   func(c worlds.Candidate, name string) error // after copying, e.g. rename inside the save
}

// Live is one world as it is on disk now.
type Live struct {
	Name       string
	Size       int64
	Modified   time.Time
	NeverSaved bool
}

// New makes the world library for one instance: live worlds as f says,
// app backups in backups (Dirs.Backups) and uploads in staging (Dirs.Staging).
//
// Call it once per instance (for example in Game.New) and return that same
// value from every Server.Worlds() call. A store is not just paths: it
// remembers the candidates Scan and Local found, which Import looks up by
// ID, and its lock is what makes changes happen one at a time and keeps a
// download from reading a world a restore is moving. A new store for each
// Worlds() call would make every upload fail as expired and let two changes
// to the same world run at once.
func New(f Format, backups, staging string) game.Worlds {
	return &store{
		f:        f,
		dir:      absPath(f.Dir),
		backups:  absPath(backups),
		staging:  absPath(staging),
		cands:    map[string]located{},
		unpacked: map[string]string{},
	}
}

// store is the world library of one instance.
type store struct {
	f       Format
	dir     string // f.Dir, absolute
	backups string
	staging string

	// mu lets one change to the worlds happen at a time (and keeps an
	// export from reading files that a restore is moving).
	mu sync.Mutex

	// cmu guards the candidates found by Scan and Local, and the zips Scan
	// has unpacked.
	cmu      sync.Mutex
	cands    map[string]located // by candidate ID
	unpacked map[string]string  // a zip (path, size, time) -> where it was unpacked
	seq      int
}

func absPath(p string) string {
	if p == "" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return filepath.Clean(p)
}

// formatError says why this store can't work, or nil. A game's format is
// fixed in its code, so this only ever fails while a game is being written.
func (s *store) formatError() error {
	switch {
	case s.dir == "":
		return errors.New("this game's world folder isn't set")
	case s.f.Kind != KindFolder && s.f.Kind != KindFiles:
		return fmt.Errorf("this game's world layout %q isn't one the app knows", s.f.Kind)
	case s.f.Kind == KindFiles && (len(s.f.Ext) == 0 || s.f.Ext[0] == ""):
		return errors.New("this game's world files have no extension set")
	}
	return nil
}

// hidden reports names the store never takes for a world: dot files (the
// store's own .egs-… folders among them) and macOS's zip leftovers.
func hidden(name string) bool {
	return strings.HasPrefix(name, ".") || name == "__MACOSX"
}

// junk reports files that don't make a folder a world: hidden ones and the
// folder settings Windows writes on its own.
func junk(name string) bool {
	return hidden(name) || strings.EqualFold(name, "desktop.ini") || strings.EqualFold(name, "thumbs.db")
}

// safeElem reports whether name can be used as one file or folder name
// inside another folder, so joining it can never reach outside that folder.
// ':' is refused too: on Windows it would name a hidden stream of a file.
func safeElem(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\:`+"\x00") && filepath.IsLocal(name)
}

// ours reports the app's own folders, which are never worlds even if a
// game's Dir holds them.
func (s *store) ours(path string) bool {
	return strings.EqualFold(path, s.backups) || strings.EqualFold(path, s.staging)
}

// liveNames lists the names of the worlds in Dir, without reading their
// files (cheap enough to call before every change).
func (s *store) liveNames() ([]string, error) {
	if err := s.formatError(); err != nil {
		return nil, err
	}
	if s.f.List != nil {
		live, err := s.f.List()
		names := make([]string, 0, len(live))
		for _, l := range live {
			names = append(names, l.Name)
		}
		return names, err
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if hidden(n) || s.ours(filepath.Join(s.dir, n)) {
			continue
		}
		switch s.f.Kind {
		case KindFolder:
			if e.IsDir() {
				names = append(names, n)
			}
		case KindFiles:
			// The server writes its own extension, so its letter case
			// matches here (uploads are matched in any case).
			if e.Type().IsRegular() && strings.HasSuffix(n, s.f.Ext[0]) && len(n) > len(s.f.Ext[0]) {
				names = append(names, strings.TrimSuffix(n, s.f.Ext[0]))
			}
		}
	}
	return names, nil
}

// live lists the worlds with their size and last save.
func (s *store) live() ([]Live, error) {
	if s.f.List != nil {
		if err := s.formatError(); err != nil {
			return nil, err
		}
		return s.f.List()
	}
	names, err := s.liveNames()
	if err != nil {
		return nil, err
	}
	out := make([]Live, 0, len(names))
	for _, n := range names {
		out = append(out, s.info(n))
	}
	return out, nil
}

// info is one world's size and last save, read from its files.
func (s *store) info(name string) Live {
	l := Live{Name: name}
	switch s.f.Kind {
	case KindFolder:
		dir := filepath.Join(s.dir, name)
		var files int
		l.Size, l.Modified, files = treeInfo(dir)
		l.NeverSaved = files == 0
		if l.Modified.IsZero() {
			if st, err := os.Stat(dir); err == nil {
				l.Modified = st.ModTime()
			}
		}
	case KindFiles:
		for _, ext := range s.f.Ext {
			if st, err := os.Lstat(filepath.Join(s.dir, name+ext)); err == nil && st.Mode().IsRegular() {
				l.Size += st.Size()
				if st.ModTime().After(l.Modified) {
					l.Modified = st.ModTime()
				}
			}
		}
	}
	return l
}

// treeInfo adds up the regular files under dir: their total size, the
// newest change and how many there are.
func treeInfo(dir string) (size int64, mod time.Time, files int) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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

// mainExists reports whether the file or folder that makes a world exist is
// there. On Windows and macOS letter case doesn't count, so "forest" finds
// the world saved as "Forest".
func (s *store) mainExists(name string) bool {
	if !safeElem(name) || s.formatError() != nil {
		return false
	}
	switch s.f.Kind {
	case KindFolder:
		st, err := os.Lstat(filepath.Join(s.dir, name))
		return err == nil && st.IsDir()
	case KindFiles:
		st, err := os.Lstat(filepath.Join(s.dir, name+s.f.Ext[0]))
		return err == nil && st.Mode().IsRegular()
	}
	return false
}

// liveName reports whether a world called name exists, and the name it is
// saved under: where letter case doesn't count in file names, replacing
// "forest" means backing up and moving "Forest".
func (s *store) liveName(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	names, err := s.liveNames()
	if err != nil {
		return "", false
	}
	for _, n := range names {
		if n == name {
			return n, true
		}
	}
	if s.f.List == nil && !s.mainExists(name) {
		return "", false
	}
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	if s.f.List != nil {
		return "", false
	}
	return name, true
}

// List returns all worlds (sorted by name) and worlds that only have backups.
func (s *store) List() ([]worlds.World, []worlds.Orphan, error) {
	live, err := s.live()
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	out := make([]worlds.World, 0, len(live))
	for _, l := range live {
		have[l.Name] = true
		out = append(out, worlds.World{
			Name: l.Name, Format: s.f.Kind, Size: l.Size, Modified: l.Modified,
			NeverSaved: l.NeverSaved, Backups: s.appBackups(l.Name),
		})
	}
	var orphans []worlds.Orphan
	if entries, err := os.ReadDir(s.backups); err == nil {
		for _, e := range entries {
			if !e.IsDir() || have[e.Name()] {
				continue
			}
			if b := s.appBackups(e.Name()); len(b) > 0 {
				orphans = append(orphans, worlds.Orphan{Name: e.Name(), Backups: b})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	sort.Slice(orphans, func(i, j int) bool {
		return strings.ToLower(orphans[i].Name) < strings.ToLower(orphans[j].Name)
	})
	return out, orphans, nil
}

// Get returns one world.
func (s *store) Get(name string) (worlds.World, error) {
	list, _, err := s.List()
	if err != nil {
		return worlds.World{}, err
	}
	for _, w := range list {
		if w.Name == name {
			return w, nil
		}
	}
	return worlds.World{}, worlds.ErrNotFound
}

// Exists reports whether a world has this name (apart from letter case
// where the file system ignores it).
func (s *store) Exists(name string) bool {
	_, ok := s.liveName(name)
	return ok
}

// entries lists what makes up a world now, as paths relative to Dir: its
// folder, its files, or what Format.Files says. Missing ones are left out.
func (s *store) entries(name string) ([]string, error) {
	if err := s.formatError(); err != nil {
		return nil, err
	}
	if s.f.Files != nil {
		paths, err := s.f.Files(name)
		if err != nil {
			return nil, err
		}
		var rels []string
		for _, p := range paths {
			rel, err := s.relInside(p)
			if err != nil {
				return nil, err
			}
			if _, err := os.Lstat(filepath.Join(s.dir, rel)); err == nil {
				rels = append(rels, rel)
			}
		}
		return outermost(rels), nil
	}
	if !safeElem(name) {
		return nil, nil
	}
	switch s.f.Kind {
	case KindFolder:
		if st, err := os.Lstat(filepath.Join(s.dir, name)); err == nil && st.IsDir() {
			return []string{name}, nil
		}
	case KindFiles:
		var rels []string
		for _, ext := range s.f.Ext {
			if st, err := os.Lstat(filepath.Join(s.dir, name+ext)); err == nil && st.Mode().IsRegular() {
				rels = append(rels, name+ext)
			}
		}
		return rels, nil
	}
	return nil, nil
}

// relInside turns a path from Format.Files into one relative to Dir,
// refusing any that isn't inside it: the store never touches anything else.
func (s *store) relInside(p string) (string, error) {
	rel := filepath.Clean(p)
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(s.dir, p)
		if err != nil {
			return "", fmt.Errorf("a world's files must be inside its worlds folder, and %s isn't", p)
		}
		rel = r
	}
	if rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("a world's files must be inside its worlds folder, and %s isn't", p)
	}
	return rel, nil
}

// outermost drops repeats and paths inside another one in the list, so
// each file is moved or zipped once.
func outermost(rels []string) []string {
	inside := func(r, outer string) bool {
		return r == outer || strings.HasPrefix(r, outer+string(filepath.Separator))
	}
	var out []string
	for i, r := range rels {
		keep := true
		for j, o := range rels {
			if (j < i && r == o) || (r != o && inside(r, o)) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

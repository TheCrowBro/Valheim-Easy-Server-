package demo

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// worldList is Demo Game's worlds: each a folder in Saves holding
// world.json, with backups as folders in Backups/<world>/<id>. Real games
// use internal/worldstore; the demo keeps this small copy-based version so
// it runs before, and apart from, that package.
type worldList struct {
	dir     string // the live worlds
	backups string
}

const maxWorldName = 40

func (w *worldList) path(name string) string { return filepath.Join(w.dir, name) }

func (w *worldList) List() ([]worlds.World, []worlds.Orphan, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	var live []worlds.World
	names := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() && w.Exists(e.Name()) {
			if wd, err := w.Get(e.Name()); err == nil {
				live = append(live, wd)
				names[e.Name()] = true
			}
		}
	}
	var orphans []worlds.Orphan
	bs, _ := os.ReadDir(w.backups)
	for _, e := range bs {
		if e.IsDir() && !names[e.Name()] {
			if b := w.backupsOf(e.Name()); len(b) > 0 {
				orphans = append(orphans, worlds.Orphan{Name: e.Name(), Backups: b})
			}
		}
	}
	return live, orphans, nil
}

func (w *worldList) Get(name string) (worlds.World, error) {
	if !w.Exists(name) {
		return worlds.World{}, fs.ErrNotExist
	}
	size, mod := treeSize(w.path(name))
	return worlds.World{Name: name, Format: "demo", Size: size, Modified: mod, Backups: w.backupsOf(name)}, nil
}

func (w *worldList) Exists(name string) bool {
	if w.NameProblem(name) != "" {
		return false
	}
	_, err := os.Stat(filepath.Join(w.path(name), "world.json"))
	return err == nil
}

func (w *worldList) NameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "Give the world a name."
	case len([]rune(name)) > maxWorldName:
		return "A world name can be at most 40 characters."
	case strings.ContainsAny(name, `\/:*?"<>|`) || strings.TrimSpace(name) != name || strings.HasPrefix(name, ".") || strings.HasSuffix(name, "."):
		return `A world name can't contain \ / : * ? " < > | or start or end with a dot or a space.`
	}
	return ""
}

// backupsOf lists a world's backups, newest first.
func (w *worldList) backupsOf(name string) []worlds.Backup {
	entries, _ := os.ReadDir(filepath.Join(w.backups, name))
	var out []worlds.Backup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		stamp, reason, _ := strings.Cut(e.Name(), "_")
		at, err := time.ParseInLocation("20060102-150405.000", stamp, time.Local)
		if err != nil {
			continue
		}
		size, _ := treeSize(filepath.Join(w.backups, name, e.Name()))
		out = append(out, worlds.Backup{ID: e.Name(), Kind: reason, Label: reason, Time: at, Size: size, Format: "demo"})
	}
	slices.SortFunc(out, func(a, b worlds.Backup) int { return b.Time.Compare(a.Time) })
	return out
}

func (w *worldList) Backup(name, reason string) (worlds.Backup, error) {
	if !w.Exists(name) {
		return worlds.Backup{}, fs.ErrNotExist
	}
	id := time.Now().Format("20060102-150405.000") + "_" + reason
	if err := copyTree(w.path(name), filepath.Join(w.backups, name, id)); err != nil {
		return worlds.Backup{}, err
	}
	for _, b := range w.backupsOf(name) {
		if b.ID == id {
			return b, nil
		}
	}
	return worlds.Backup{}, errors.New("the backup wasn't kept")
}

// Restore replaces the world with a backup. The world is set aside first
// and put back if the copy fails, so a failed restore loses nothing.
func (w *worldList) Restore(name, backupID string) error {
	src := filepath.Join(w.backups, name, backupID)
	if backupID == "" || strings.ContainsAny(backupID, `\/`) {
		return fs.ErrNotExist
	}
	if _, err := os.Stat(src); err != nil {
		return err
	}
	live := w.path(name)
	aside := live + ".restoring"
	os.RemoveAll(aside)
	if err := os.Rename(live, aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := copyTree(src, live); err != nil {
		os.RemoveAll(live)
		os.Rename(aside, live)
		return err
	}
	return os.RemoveAll(aside)
}

func (w *worldList) Delete(name string) error {
	if !w.Exists(name) {
		return fs.ErrNotExist
	}
	return os.RemoveAll(w.path(name))
}

func (w *worldList) Rename(name, newName string) error {
	if p := w.NameProblem(newName); p != "" {
		return errors.New(p)
	}
	if w.Exists(newName) {
		return errors.New("there is already a world called " + newName)
	}
	if err := os.Rename(w.path(name), w.path(newName)); err != nil {
		return err
	}
	os.Rename(filepath.Join(w.backups, name), filepath.Join(w.backups, newName))
	return nil
}

func (w *worldList) ExportFile(name, dir string) (string, error) { return "", errors.ErrUnsupported }

func (w *worldList) ExportBackup(out io.Writer, name, id string) error { return errors.ErrUnsupported }

func (w *worldList) Scan(dir string) []worlds.Candidate { return nil }

func (w *worldList) Local() []worlds.Candidate { return nil }

func (w *worldList) Import(c []worlds.Candidate, ch []worlds.Choice, busy func(string) bool, move bool) ([]worlds.Placed, error) {
	return nil, errors.ErrUnsupported
}

func (w *worldList) CleanStaging(maxAge time.Duration) {}

// treeSize is the size of every file under dir and the newest change.
func treeSize(dir string) (size int64, newest time.Time) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
				if info.ModTime().After(newest) {
					newest = info.ModTime()
				}
			}
		}
		return nil
	})
	return size, newest
}

// copyTree copies the folder from into a new folder to.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

var _ game.Worlds = (*worldList)(nil)

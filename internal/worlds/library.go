package worlds

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
)

// Formats.
const (
	FormatFolder  = "folder"  // Valheim 1.0 and later
	FormatClassic = "classic" // .fwl + .db, converted by the server on first load
)

// Backup kinds.
const (
	KindAuto      = "auto"     // Valheim's automatic backups
	KindValheim   = "copy"     // other copies Valheim made (e.g. before converting to 1.0)
	KindPrevious  = "previous" // .fwl.old/.db.old
	KindApp       = "app"      // made by this app
	keepAppBackup = 20         // newest app backups kept per world
)

// World is one world in the server's save folder.
type World struct {
	Name       string    `json:"name"`
	Format     string    `json:"format"`
	Size       int64     `json:"size"`
	Modified   time.Time `json:"modified"`
	Seed       string    `json:"seed,omitempty"`
	NeverSaved bool      `json:"neverSaved,omitempty"`
	Shadowed   bool      `json:"shadowed,omitempty"` // an old-format copy with the same name is ignored
	Backups    []Backup  `json:"backups"`
}

// Backup is a restorable copy of a world.
type Backup struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Label  string    `json:"label"`
	Time   time.Time `json:"time"`
	Size   int64     `json:"size"`
	Format string    `json:"format"`

	path string // folder, zip file, or classic path without extension
}

// Orphan is a world that only exists as backups (e.g. after deleting it).
type Orphan struct {
	Name    string   `json:"name"`
	Backups []Backup `json:"backups"`
}

// Library manages the worlds folder. Its folders must not change once it
// is in use; make a new Library for other folders.
type Library struct {
	Worlds  string // the server's worlds_local folder
	Backups string // this app's backup folder
	Staging string // where uploads wait to be imported

	mu sync.Mutex
}

// ErrNotFound means there is no world with that name.
var ErrNotFound = errors.New("there's no world with that name")

var reBackupName = regexp.MustCompile(`^(.+?)_backup_(.*)$`)

func parseStamp(s string, fallback time.Time) time.Time {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) >= 14 {
		if t, err := time.ParseInLocation("20060102150405", digits[:14], time.Local); err == nil {
			return t
		}
	}
	return fallback
}

func backupLabel(kind string) string {
	switch kind {
	case KindAuto:
		return "Automatic backup by Valheim"
	case KindValheim:
		return "Copy saved by Valheim"
	case KindPrevious:
		return "Previous save (old format)"
	}
	return "Backup"
}

type scanResult struct {
	worlds  map[string]*World
	backups map[string][]Backup // world name -> Valheim's own backups
}

func (l *Library) scan() (scanResult, error) {
	res := scanResult{worlds: map[string]*World{}, backups: map[string][]Backup{}}
	entries, err := os.ReadDir(l.Worlds)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	classic := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(l.Worlds, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			if m := reBackupName.FindStringSubmatch(name); m != nil {
				if size, mod, ok := folderWorldInfo(full); ok {
					kind := KindValheim
					if strings.HasPrefix(m[2], "auto") {
						kind = KindAuto
					}
					res.backups[m[1]] = append(res.backups[m[1]], Backup{
						ID: "v:" + name, Kind: kind, Label: backupLabel(kind), Format: FormatFolder,
						Time: parseStamp(m[2], mod), Size: size, path: full,
					})
				}
				continue
			}
			if size, mod, ok := folderWorldInfo(full); ok {
				w := &World{Name: name, Format: FormatFolder, Size: size, Modified: mod}
				if meta, ok := folderMeta(full); ok {
					w.Seed = meta.SeedName
				}
				res.worlds[name] = w
			}
			continue
		}
		switch {
		case strings.HasSuffix(name, ".fwl.old"):
			base := strings.TrimSuffix(name, ".fwl.old")
			b := Backup{ID: "v:" + base + ".old", Kind: KindPrevious, Label: backupLabel(KindPrevious),
				Format: FormatClassic, Time: info.ModTime(), Size: info.Size(), path: filepath.Join(l.Worlds, base)}
			if db, err := os.Stat(filepath.Join(l.Worlds, base+".db.old")); err == nil {
				b.Size += db.Size()
				b.Time = db.ModTime()
			}
			res.backups[base] = append(res.backups[base], b)
		case strings.HasSuffix(name, ".fwl"):
			base := strings.TrimSuffix(name, ".fwl")
			if m := reBackupName.FindStringSubmatch(base); m != nil {
				kind := KindValheim
				if strings.HasPrefix(m[2], "auto") {
					kind = KindAuto
				}
				size, mod := pairInfo(filepath.Join(l.Worlds, base))
				res.backups[m[1]] = append(res.backups[m[1]], Backup{
					ID: "v:" + base, Kind: kind, Label: backupLabel(kind), Format: FormatClassic,
					Time: parseStamp(m[2], mod), Size: size, path: filepath.Join(l.Worlds, base),
				})
				continue
			}
			classic[base] = true
		}
	}
	for base := range classic {
		if w, ok := res.worlds[base]; ok {
			w.Shadowed = true // the 1.0 folder wins over an old-format pair
			continue
		}
		p := filepath.Join(l.Worlds, base)
		size, mod := pairInfo(p)
		w := &World{Name: base, Format: FormatClassic, Size: size, Modified: mod}
		if _, err := os.Stat(p + ".db"); err != nil {
			w.NeverSaved = true
		}
		if data, err := os.ReadFile(p + ".fwl"); err == nil {
			if meta, err := ParseMeta(data); err == nil {
				w.Seed = meta.SeedName
			}
		}
		res.worlds[base] = w
	}
	return res, nil
}

// folderWorldInfo reports whether dir holds a 1.0 world, with its total
// size and newest modification time.
func folderWorldInfo(dir string) (size int64, mod time.Time, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, time.Time{}, false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "_main.") || strings.HasSuffix(n, ".chunk") {
			ok = true
		}
		if info, err := e.Info(); err == nil {
			size += info.Size()
			if info.ModTime().After(mod) {
				mod = info.ModTime()
			}
		}
	}
	return size, mod, ok
}

var reMainFwl2 = regexp.MustCompile(`^_main\.(\d+)\.fwl2$`)

// folderMeta reads the newest _main.<N>.fwl2 in a world folder.
func folderMeta(dir string) (Meta, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Meta{}, false
	}
	best, bestN := "", -1
	for _, e := range entries {
		if m := reMainFwl2.FindStringSubmatch(e.Name()); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > bestN {
				best, bestN = e.Name(), n
			}
		}
	}
	if best == "" {
		return Meta{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, best))
	if err != nil {
		return Meta{}, false
	}
	meta, err := ParseMeta(data)
	return meta, err == nil
}

func pairInfo(base string) (size int64, mod time.Time) {
	for _, ext := range []string{".fwl", ".db"} {
		if info, err := os.Stat(base + ext); err == nil {
			size += info.Size()
			if info.ModTime().After(mod) {
				mod = info.ModTime()
			}
		}
	}
	return size, mod
}

// appBackups lists this app's zip backups for a world, newest first.
func (l *Library) appBackups(name string) []Backup {
	dir := filepath.Join(l.Backups, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// File names look like 20261022-201500_manual_folder.zip
		parts := strings.Split(strings.TrimSuffix(e.Name(), ".zip"), "_")
		b := Backup{ID: "app:" + e.Name(), Kind: KindApp, Size: info.Size(),
			Time: parseStamp(parts[0], info.ModTime()), path: filepath.Join(dir, e.Name()), Format: FormatFolder}
		reason := ""
		if len(parts) >= 2 {
			reason = parts[1]
		}
		if len(parts) >= 3 && parts[2] == FormatClassic {
			b.Format = FormatClassic
		}
		b.Label = appBackupLabel(reason)
		out = append(out, b)
	}
	return out
}

func appBackupLabel(reason string) string {
	switch reason {
	case "manual":
		return "Backup made in this app"
	case "before-restore":
		return "Automatic copy before a restore"
	case "before-replace":
		return "Automatic copy before it was replaced"
	case "deleted":
		return "Copy kept when the world was deleted"
	case "before-rename":
		return "Automatic copy before renaming"
	}
	return "Backup made in this app"
}

func sortBackups(b []Backup) {
	sort.Slice(b, func(i, j int) bool { return b[i].Time.After(b[j].Time) })
}

// List returns all worlds (sorted by name) and worlds that only have backups.
func (l *Library) List() ([]World, []Orphan, error) {
	res, err := l.scan()
	if err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	for n := range res.worlds {
		names[n] = true
	}
	for n := range res.backups {
		names[n] = true
	}
	if entries, err := os.ReadDir(l.Backups); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				names[e.Name()] = true
			}
		}
	}
	var worlds []World
	var orphans []Orphan
	for n := range names {
		backups := append(append([]Backup{}, res.backups[n]...), l.appBackups(n)...)
		sortBackups(backups)
		if w, ok := res.worlds[n]; ok {
			w.Backups = backups
			worlds = append(worlds, *w)
		} else if len(backups) > 0 {
			orphans = append(orphans, Orphan{Name: n, Backups: backups})
		}
	}
	sort.Slice(worlds, func(i, j int) bool { return strings.ToLower(worlds[i].Name) < strings.ToLower(worlds[j].Name) })
	sort.Slice(orphans, func(i, j int) bool { return strings.ToLower(orphans[i].Name) < strings.ToLower(orphans[j].Name) })
	return worlds, orphans, nil
}

// Get returns one world.
func (l *Library) Get(name string) (World, error) {
	worlds, _, err := l.List()
	if err != nil {
		return World{}, err
	}
	for _, w := range worlds {
		if w.Name == name {
			return w, nil
		}
	}
	return World{}, ErrNotFound
}

func (l *Library) findBackup(name, id string) (Backup, error) {
	worlds, orphans, err := l.List()
	if err != nil {
		return Backup{}, err
	}
	var list []Backup
	for _, w := range worlds {
		if w.Name == name {
			list = w.Backups
		}
	}
	for _, o := range orphans {
		if o.Name == name {
			list = o.Backups
		}
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return Backup{}, errors.New("that backup doesn't exist any more")
}

// worldEntries lists what to put in a zip for a world: zip path -> file.
func worldEntries(w World, worldsDir string) (map[string]string, error) {
	out := map[string]string{}
	switch w.Format {
	case FormatFolder:
		dir := filepath.Join(worldsDir, w.Name)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			out[w.Name+"/"+filepath.ToSlash(rel)] = p
			return nil
		})
		return out, err
	default:
		base := filepath.Join(worldsDir, w.Name)
		for _, ext := range []string{".fwl", ".db"} {
			if _, err := os.Stat(base + ext); err == nil {
				out[w.Name+ext] = base + ext
			}
		}
	}
	return out, nil
}

// backupEntries lists what to put in a zip for a Valheim backup, renamed so
// the result looks like the world itself.
func backupEntries(name string, b Backup) (map[string]string, error) {
	out := map[string]string{}
	switch {
	case b.Format == FormatFolder:
		err := filepath.WalkDir(b.path, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(b.path, p)
			if err != nil {
				return err
			}
			out[name+"/"+filepath.ToSlash(rel)] = p
			return nil
		})
		return out, err
	case b.Kind == KindPrevious:
		for _, ext := range []string{".fwl", ".db"} {
			if _, err := os.Stat(b.path + ext + ".old"); err == nil {
				out[name+ext] = b.path + ext + ".old"
			}
		}
	default:
		for _, ext := range []string{".fwl", ".db"} {
			if _, err := os.Stat(b.path + ext); err == nil {
				out[name+ext] = b.path + ext
			}
		}
	}
	return out, nil
}

// writeZip writes files (zip path -> disk path) to w, fastest compression.
func writeZip(w io.Writer, files map[string]string) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestSpeed)
	})
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := addFile(zw, n, files[n]); err != nil {
			return err
		}
	}
	return zw.Close()
}

func addFile(zw *zip.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// Export writes a world as a zip file.
func (l *Library) Export(w io.Writer, name string) error {
	world, err := l.Get(name)
	if err != nil {
		return err
	}
	files, err := worldEntries(world, l.Worlds)
	if err != nil {
		return err
	}
	return writeZip(w, files)
}

// ExportBackup writes a backup as a zip file laid out like the world.
func (l *Library) ExportBackup(w io.Writer, name, id string) error {
	b, err := l.findBackup(name, id)
	if err != nil {
		return err
	}
	if b.Kind == KindApp {
		f, err := os.Open(b.path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
	files, err := backupEntries(name, b)
	if err != nil {
		return err
	}
	return writeZip(w, files)
}

// Backup saves a zip copy of a world in the app's backup folder.
func (l *Library) Backup(name, reason string) (Backup, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backupLocked(name, reason)
}

func (l *Library) backupLocked(name, reason string) (Backup, error) {
	world, err := l.Get(name)
	if err != nil {
		return Backup{}, err
	}
	files, err := worldEntries(world, l.Worlds)
	if err != nil {
		return Backup{}, err
	}
	dir := filepath.Join(l.Backups, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Backup{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	final := filepath.Join(dir, fmt.Sprintf("%s_%s_%s.zip", stamp, reason, world.Format))
	for i := 2; fileExists(final); i++ {
		final = filepath.Join(dir, fmt.Sprintf("%s-%d_%s_%s.zip", stamp, i, reason, world.Format))
	}
	tmp, err := os.CreateTemp(dir, ".backup-*.tmp")
	if err != nil {
		return Backup{}, err
	}
	defer os.Remove(tmp.Name())
	if err := writeZip(tmp, files); err != nil {
		tmp.Close()
		return Backup{}, fmt.Errorf("couldn't write the backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Backup{}, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return Backup{}, err
	}
	l.prune(name, filepath.Base(final))
	for _, b := range l.appBackups(name) {
		if b.path == final {
			return b, nil
		}
	}
	return Backup{}, errors.New("backup written but not found")
}

// prune keeps the newest app backups of a world.
func (l *Library) prune(name, keep string) {
	backups := l.appBackups(name)
	sortBackups(backups)
	for i, b := range backups {
		if i >= keepAppBackup && filepath.Base(b.path) != keep {
			os.Remove(b.path)
		}
	}
}

// removeLive deletes a world's live files: the folder, the old-format pair
// and its .old files. Valheim's own backups are left alone.
func (l *Library) removeLive(name string) error {
	base := filepath.Join(l.Worlds, name)
	if err := os.RemoveAll(base); err != nil {
		return fmt.Errorf("couldn't remove the world folder (is the server using it?): %w", err)
	}
	for _, ext := range []string{".fwl", ".db", ".fwl.old", ".db.old"} {
		if err := os.Remove(base + ext); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("couldn't remove %s (is the server using it?): %w", name+ext, err)
		}
	}
	return nil
}

// Delete removes a world, keeping a zip backup and Valheim's own backups so
// it can be brought back.
func (l *Library) Delete(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.backupLocked(name, "deleted"); err != nil {
		return fmt.Errorf("couldn't make a safety backup, so nothing was deleted: %w", err)
	}
	return l.removeLive(name)
}

// Rename renames an old-format world (its .fwl records the name, so that is
// updated too). 1.0 worlds can't be renamed safely yet.
func (l *Library) Rename(name, newName string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if msg := config.WorldNameProblem(newName); msg != "" {
		return errors.New(msg)
	}
	world, err := l.Get(name)
	if err != nil {
		return err
	}
	if world.Format == FormatFolder {
		return errors.New("Valheim 1.0 worlds can't be renamed safely, so this app doesn't offer it.")
	}
	if l.exists(newName) {
		return fmt.Errorf("there's already a world called %q", newName)
	}
	if _, err := l.backupLocked(name, "before-rename"); err != nil {
		return err
	}
	src := filepath.Join(l.Worlds, name)
	dst := filepath.Join(l.Worlds, newName)
	data, err := os.ReadFile(src + ".fwl")
	if err != nil {
		return err
	}
	renamed, err := RenameMeta(data, newName)
	if err != nil {
		return fmt.Errorf("couldn't update the world's name inside its .fwl file: %w", err)
	}
	if err := config.WriteFileAtomic(dst+".fwl", renamed, 0o644); err != nil {
		return err
	}
	if fileExists(src + ".db") {
		if err := os.Rename(src+".db", dst+".db"); err != nil {
			os.Remove(dst + ".fwl")
			return err
		}
	}
	os.Remove(src + ".fwl")
	// Move this app's backups along with the world.
	oldDir, newDir := filepath.Join(l.Backups, name), filepath.Join(l.Backups, newName)
	if _, err := os.Stat(newDir); errors.Is(err, fs.ErrNotExist) {
		os.Rename(oldDir, newDir)
	}
	return nil
}

func (l *Library) exists(name string) bool {
	base := filepath.Join(l.Worlds, name)
	if st, err := os.Stat(base); err == nil && st.IsDir() {
		return true
	}
	return fileExists(base + ".fwl")
}

// Exists reports whether a world (either format) has this name.
func (l *Library) Exists(name string) bool { return l.exists(name) }

// Restore puts a backup back as the live world. The current world, if any,
// is backed up first.
func (l *Library) Restore(name, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := l.findBackup(name, id)
	if err != nil {
		return err
	}
	// Prepare the restored files first, so a failure leaves the world as is.
	c, tmp, err := l.stageBackup(name, b)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if l.exists(name) {
		if _, err := l.backupLocked(name, "before-restore"); err != nil {
			return fmt.Errorf("couldn't back up the current world first, so nothing was changed: %w", err)
		}
		if err := l.removeLive(name); err != nil {
			return err
		}
	}
	return place(c, name, l.Worlds, true)
}

// putBack restores a backup of a world that has just been removed.
func (l *Library) putBack(name string, b Backup) error {
	c, tmp, err := l.stageBackup(name, b)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	return place(c, name, l.Worlds, true)
}

// stageBackup unpacks or copies a backup into a new temporary folder and
// returns the world found there. The caller removes the folder.
func (l *Library) stageBackup(name string, b Backup) (Candidate, string, error) {
	tmp, err := os.MkdirTemp(filepath.Dir(l.Worlds), ".restore-")
	if err != nil {
		return Candidate{}, "", err
	}
	fail := func(err error) (Candidate, string, error) {
		os.RemoveAll(tmp)
		return Candidate{}, "", err
	}
	if b.Kind == KindApp {
		if err := extractZip(b.path, tmp, 0); err != nil {
			return fail(fmt.Errorf("the backup file is damaged: %w", err))
		}
	} else {
		files, err := backupEntries(name, b)
		if err != nil {
			return fail(err)
		}
		for rel, src := range files {
			if err := copyFile(src, filepath.Join(tmp, filepath.FromSlash(rel))); err != nil {
				return fail(err)
			}
		}
	}
	cands := Detect(tmp, 2)
	if len(cands) != 1 {
		return fail(errors.New("the backup doesn't contain exactly one world"))
	}
	return cands[0], tmp, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// copyFile copies src to dst, creating folders as needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

// copyDir copies a folder tree.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target)
	})
}

// moveOrCopy renames when possible (same drive) and copies otherwise.
func moveOrCopy(src, dst string, isDir bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if isDir {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

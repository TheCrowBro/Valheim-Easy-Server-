package worldstore

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// keepBackups is how many of the app's backups are kept per world; older
// ones are deleted when a new one is made.
const keepBackups = 20

// Hidden folders inside Dir where a world's files wait during a change.
const (
	removingPrefix = ".egs-removing-" // the old copy, until the new one is in place
	restorePrefix  = ".egs-restore-"  // a backup being unpacked
	incomingPrefix = ".egs-incoming-" // an imported world being copied in
)

// renameFile is os.Rename; tests change it to make a move fail, as a file
// another program holds open does on Windows.
var renameFile = os.Rename

// retryDelay is how long a copy of a world that changed while it was being
// zipped waits for the save to finish before trying again.
var retryDelay = 2 * time.Second

// zipping, when set, runs after a world's files are listed and just before
// they are zipped, so tests can save the world at the worst moment.
var zipping func()

var errNoBackup = errors.New("that backup doesn't exist any more")

// cleanReason keeps a backup's reason safe for its file name, where "_"
// separates the parts.
func cleanReason(reason string) string {
	r := strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			return c
		case c >= 'A' && c <= 'Z':
			return c - 'A' + 'a'
		}
		return -1
	}, reason)
	if r == "" {
		return "manual"
	}
	return r
}

func backupLabel(reason string) string {
	switch reason {
	case "manual":
		return "Backup made in this app"
	case "auto":
		return "Automatic backup made by this app"
	case "before-restore":
		return "Automatic copy before a restore"
	case "before-replace":
		return "Automatic copy before it was replaced"
	case "deleted":
		return "Copy kept when the world was deleted"
	case "before-rename":
		return "Automatic copy before renaming"
	case "removed":
		return "Copy kept when the game was removed"
	}
	return "Backup made in this app"
}

// parseStamp reads the time at the start of a backup's file name
// (20261022-201500), or returns fallback.
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

// appBackups lists a world's backups, newest first.
func (s *store) appBackups(name string) []worlds.Backup {
	out := []worlds.Backup{}
	if !safeElem(name) || s.backups == "" {
		return out
	}
	entries, err := os.ReadDir(filepath.Join(s.backups, name))
	if err != nil {
		return out
	}
	type sorted struct {
		b   worlds.Backup
		seq int // 2 for the second backup made in the same second, and so on
	}
	var list []sorted
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// File names look like 20261022-201500_manual.zip, or
		// 20261022-201500-2_manual.zip for a second one that second.
		stamp, reason, _ := strings.Cut(strings.TrimSuffix(e.Name(), ".zip"), "_")
		seq := 1
		if parts := strings.Split(stamp, "-"); len(parts) == 3 {
			if n, err := strconv.Atoi(parts[2]); err == nil {
				seq = n
			}
		}
		list = append(list, sorted{worlds.Backup{
			ID: "app:" + e.Name(), Kind: worlds.KindApp, Label: backupLabel(reason),
			Time: parseStamp(stamp, info.ModTime()), Size: info.Size(), Format: s.f.Kind,
		}, seq})
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].b.Time.Equal(list[j].b.Time) {
			return list[i].b.Time.After(list[j].b.Time)
		}
		return list[i].seq > list[j].seq
	})
	for _, l := range list {
		out = append(out, l.b)
	}
	return out
}

// backupPath finds a backup's zip from its ID, refusing any ID that could
// name a file outside the world's backup folder.
func (s *store) backupPath(name, id string) (string, error) {
	file, ok := strings.CutPrefix(id, "app:")
	if !ok || !safeElem(name) || !safeElem(file) || !strings.HasSuffix(file, ".zip") || s.backups == "" {
		return "", errNoBackup
	}
	p := filepath.Join(s.backups, name, file)
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return "", errNoBackup
	}
	return p, nil
}

// Backup saves a zip copy of a world in the app's backup folder. The world
// may be running: if the server saves while it is being copied, the copy
// starts again, so it is never half old and half new.
func (s *store) Backup(name, reason string) (worlds.Backup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, ok := s.liveName(name)
	if !ok {
		return worlds.Backup{}, worlds.ErrNotFound
	}
	return s.backupLocked(saved, reason)
}

// backupLocked zips whatever is saved under name now. s.mu is held.
func (s *store) backupLocked(name, reason string) (worlds.Backup, error) {
	if !safeElem(name) {
		return worlds.Backup{}, fmt.Errorf("the world name %q can't be used for its backup folder", name)
	}
	if s.backups == "" {
		return worlds.Backup{}, errors.New("this game's backup folder isn't set")
	}
	rels, err := s.entries(name)
	if err != nil {
		return worlds.Backup{}, err
	}
	if len(rels) == 0 {
		return worlds.Backup{}, worlds.ErrNotFound
	}
	dir := filepath.Join(s.backups, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return worlds.Backup{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	reason = cleanReason(reason)
	file := fmt.Sprintf("%s_%s.zip", stamp, reason)
	for i := 2; exists(filepath.Join(dir, file)); i++ {
		file = fmt.Sprintf("%s-%d_%s.zip", stamp, i, reason)
	}
	tmp, err := s.zipWorld(name, dir, ".backup-*.tmp")
	if err != nil {
		return worlds.Backup{}, fmt.Errorf("couldn't write the backup: %w", err)
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, filepath.Join(dir, file)); err != nil {
		return worlds.Backup{}, err
	}
	s.prune(name, file)
	for _, b := range s.appBackups(name) {
		if b.ID == "app:"+file {
			return b, nil
		}
	}
	return worlds.Backup{}, errors.New("the backup was written but can't be found")
}

// dropBackup deletes a safety backup made for a change that then changed
// nothing, so the list doesn't fill with copies of a world still in place.
func (s *store) dropBackup(name, id string) {
	if p, err := s.backupPath(name, id); err == nil {
		os.Remove(p)
	}
}

// prune keeps the newest backups of a world, and the one just made.
func (s *store) prune(name, keep string) {
	dir := filepath.Join(s.backups, name)
	for i, b := range s.appBackups(name) {
		if file := strings.TrimPrefix(b.ID, "app:"); i >= keepBackups && file != keep {
			os.Remove(filepath.Join(dir, file))
		}
	}
	// A backup cut short (the app closed mid-copy) leaves its temporary file.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".backup-") {
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
					os.Remove(filepath.Join(dir, e.Name()))
				}
			}
		}
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// zipList lists what goes in a world's zip: zip path -> file on disk, or ""
// for a folder (kept, so empty folders come back too). Zip paths are the
// world's place relative to Dir.
func (s *store) zipList(name string) (map[string]string, error) {
	rels, err := s.entries(name)
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, worlds.ErrNotFound
	}
	out := map[string]string{}
	for _, rel := range rels {
		root := filepath.Join(s.dir, rel)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			r, err := filepath.Rel(s.dir, p)
			if err != nil {
				return err
			}
			zp := filepath.ToSlash(r)
			switch {
			case d.IsDir():
				out[zp+"/"] = ""
			case d.Type().IsRegular():
				out[zp] = p
			}
			return nil // links and other special files aren't copied
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fingerprint sums up files' names, sizes and times, to notice a save.
func fingerprint(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		if files[n] == "" {
			fmt.Fprintf(&b, "%s\n", n)
			continue
		}
		st, err := os.Stat(files[n])
		if err != nil {
			fmt.Fprintf(&b, "%s missing\n", n)
			continue
		}
		fmt.Fprintf(&b, "%s %d %d\n", n, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

// zipWorld zips a world into a new temporary file in dir and returns its
// path. The world may be running, so if its files change while they are
// being zipped (the server saved), it starts again: the copy is never half
// old and half new, and a file a save deleted doesn't fail it.
func (s *store) zipWorld(name, dir, pattern string) (string, error) {
	for try := 0; try < 3; try++ {
		files, err := s.zipList(name)
		if err != nil {
			return "", err
		}
		before := fingerprint(files)
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return "", err
		}
		if zipping != nil {
			zipping()
		}
		err = writeZip(f, files)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			if after, aerr := s.zipList(name); aerr == nil && fingerprint(after) == before {
				return f.Name(), nil
			}
		}
		os.Remove(f.Name())
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err // a real problem, not a file replaced by a save
		}
		time.Sleep(retryDelay) // let the save finish
	}
	return "", errors.New("the server kept saving the world while it was being copied. Try again in a minute")
}

// writeZip writes files (zip path -> disk path, "" for a folder) to w, with
// the fastest compression: worlds are big and mostly compressed already.
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
		if files[n] == "" {
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store, Modified: time.Now()}); err != nil {
				return err
			}
			continue
		}
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

// ExportFile zips a world into a new file in dir and returns its path. The
// zip holds the world laid out as the game keeps it, so dropping it on "Add
// a world" brings it back. It holds the store's lock while it reads the
// world, so a download can't keep a file open while a restore, replace or
// delete is moving the world's files.
func (s *store) ExportFile(name, dir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, ok := s.liveName(name)
	if !ok {
		return "", worlds.ErrNotFound
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return s.zipWorld(saved, dir, ".download-*.zip")
}

// ExportBackup writes a backup's zip to w as it is.
func (s *store) ExportBackup(w io.Writer, name, id string) error {
	p, err := s.backupPath(name, id)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// aside is a world taken out of Dir by setAside.
type aside struct {
	dir   string
	moves [][2]string // from, to
}

// setAside moves a world's files (paths relative to Dir) into a new hidden
// folder in Dir. Every caller has just made safety, a backup of the world
// called name, in case the change goes wrong.
//
// Moving is all or nothing, unlike deleting: on Windows a file another
// program has open (a download, a backup tool, the server) makes its move
// fail, and then everything already moved goes back, so the world is left
// exactly as it was. safety is then only a copy of a world still in place,
// so it is deleted to keep the backup list tidy.
//
// If a file can't be put back either, the world is left half in the hidden
// folder, and the next change deletes that folder (clearAsides). safety is
// then the only whole copy of the world, so it is kept, and the error says
// to restore from it rather than claiming nothing changed.
func (s *store) setAside(rels []string, name string, safety worlds.Backup) (*aside, error) {
	a := &aside{}
	if len(rels) == 0 {
		return a, nil
	}
	dir, err := os.MkdirTemp(s.dir, removingPrefix)
	if err != nil {
		s.dropBackup(name, safety.ID)
		return nil, err
	}
	a.dir = dir
	for _, rel := range rels {
		from, to := filepath.Join(s.dir, rel), filepath.Join(dir, rel)
		err := os.MkdirAll(filepath.Dir(to), 0o755)
		if err == nil {
			err = renameFile(from, to)
		}
		if err != nil {
			if uerr := a.undo(); uerr != nil {
				return nil, fmt.Errorf("couldn't move %s out of the way (%v), and putting back the files already moved failed too (%v), so the world is only partly there. Restore it from its %q backup.", rel, err, uerr, safety.Label)
			}
			s.dropBackup(name, safety.ID)
			return nil, fmt.Errorf("couldn't move %s out of the way (is the server or another program using it?), so nothing was changed: %w", rel, err)
		}
		a.moves = append(a.moves, [2]string{from, to})
	}
	return a, nil
}

// undo puts a world taken out by setAside back as it was.
func (a *aside) undo() error {
	var first error
	for i := len(a.moves) - 1; i >= 0; i-- {
		if err := renameFile(a.moves[i][1], a.moves[i][0]); err != nil && first == nil {
			first = err
		}
	}
	if first == nil && a.dir != "" {
		os.RemoveAll(a.dir)
	}
	return first
}

// drop deletes the old files once the change is done. It is only tidying:
// a file that can't be deleted yet is cleared with the hidden folder the
// next time the worlds change.
func (a *aside) drop() {
	if a.dir != "" {
		os.RemoveAll(a.dir)
	}
}

// clearAsides deletes hidden folders an earlier change left in Dir: a drop
// that failed, a world that couldn't be put back whole, or the app closing
// in the middle of a change. Every change makes a backup before it moves a
// world, and keeps it unless the world was put back whole, so nothing is
// lost with them.
func (s *store) clearAsides() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && (strings.HasPrefix(n, removingPrefix) || strings.HasPrefix(n, restorePrefix) || strings.HasPrefix(n, incomingPrefix)) {
			os.RemoveAll(filepath.Join(s.dir, n))
		}
	}
}

// mergeMove moves what src holds into dst at the same relative places:
// whatever isn't in dst yet is moved whole, and folders both have are
// merged. It never replaces anything already in dst. It returns the moves
// made, so placeBack can undo them.
func mergeMove(src, dst string) ([][2]string, error) {
	var moves [][2]string
	var merge func(src, dst string) error
	merge = func(src, dst string) error {
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
			st, err := os.Lstat(to)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				if err := renameFile(from, to); err != nil {
					return fmt.Errorf("couldn't put %s in place: %w", e.Name(), err)
				}
				moves = append(moves, [2]string{from, to})
			case err != nil:
				return err
			case st.IsDir() && e.IsDir():
				if err := merge(from, to); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s is already in the worlds folder", e.Name())
			}
		}
		return nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	return moves, merge(src, dst)
}

// placeBack undoes mergeMove: each moved file goes back where it came from,
// or is deleted if it can't move (it is a copy that didn't exist before).
func placeBack(moves [][2]string) {
	for i := len(moves) - 1; i >= 0; i-- {
		if err := renameFile(moves[i][1], moves[i][0]); err != nil {
			os.RemoveAll(moves[i][1])
		}
	}
}

// Restore puts a backup back as the live world. The current world, if
// any, is backed up first, and is put back if the backup can't be.
func (s *store) Restore(name, backupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.formatError(); err != nil {
		return err
	}
	zipPath, err := s.backupPath(name, backupID)
	if err != nil {
		return err
	}
	target := name
	if saved, ok := s.liveName(name); ok {
		target = saved
	}
	s.clearAsides()
	// Unpack first, so a damaged backup leaves the world as it is.
	staged, tmp, err := s.stageBackup(zipPath, target)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	rels, err := s.entries(target)
	if err != nil {
		return err
	}
	var safety worlds.Backup
	if len(rels) > 0 {
		safety, err = s.backupLocked(target, "before-restore")
		if err != nil {
			return fmt.Errorf("couldn't back up the current world first, so nothing was changed: %w", err)
		}
	}
	a, err := s.setAside(rels, target, safety)
	if err != nil {
		return err
	}
	moves, err := mergeMove(staged, s.dir)
	if err != nil {
		placeBack(moves)
		if uerr := a.undo(); uerr != nil {
			return fmt.Errorf("couldn't restore the backup (%v), and putting the current world back failed too (%v). Restore it from its %q backup.", err, uerr, safety.Label)
		}
		s.dropBackup(target, safety.ID)
		return fmt.Errorf("couldn't restore the backup, so the world was left as it was: %w", err)
	}
	a.drop()
	return nil
}

// stageBackup unpacks a backup into a new hidden folder in Dir and lays it
// out as the world called name would be in Dir. It returns that layout and
// the folder to delete afterwards. A backup made before a rename holds the
// old name; it comes back under the world's name now.
func (s *store) stageBackup(zipPath, name string) (staged, tmp string, err error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", "", err
	}
	tmp, err = os.MkdirTemp(s.dir, restorePrefix)
	if err != nil {
		return "", "", err
	}
	fail := func(err error) (string, string, error) {
		os.RemoveAll(tmp)
		return "", "", err
	}
	raw := filepath.Join(tmp, "raw")
	if err := unzip(zipPath, raw, 0); err != nil {
		return fail(fmt.Errorf("the backup file is damaged: %w", err))
	}
	if s.f.Files != nil {
		// Laid out as in Dir already; these worlds are never renamed.
		return raw, tmp, nil
	}
	out := filepath.Join(tmp, "world")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fail(err)
	}
	entries, err := os.ReadDir(raw)
	if err != nil {
		return fail(err)
	}
	var visible []fs.DirEntry
	for _, e := range entries {
		if !junk(e.Name()) {
			visible = append(visible, e)
		}
	}
	notOne := errors.New("the backup doesn't hold exactly one world")
	switch s.f.Kind {
	case KindFolder:
		if len(visible) != 1 || !visible[0].IsDir() {
			return fail(notOne)
		}
		if err := os.Rename(filepath.Join(raw, visible[0].Name()), filepath.Join(out, name)); err != nil {
			return fail(err)
		}
	case KindFiles:
		var found []srcFile
		base := ""
		for _, e := range visible {
			if !e.Type().IsRegular() {
				continue
			}
			b, ok := strings.CutSuffix(e.Name(), s.f.Ext[0])
			if !ok || b == "" {
				continue
			}
			if base != "" {
				return fail(notOne)
			}
			base = b
		}
		if base == "" {
			return fail(notOne)
		}
		for _, ext := range s.f.Ext {
			if p := filepath.Join(raw, base+ext); exists(p) {
				found = append(found, srcFile{path: p, ext: ext})
			}
		}
		for _, f := range found {
			if err := os.Rename(f.path, filepath.Join(out, name+f.ext)); err != nil {
				return fail(err)
			}
		}
	}
	return out, tmp, nil
}

// Delete removes a world, keeping a zip backup so it can be brought back.
func (s *store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, ok := s.liveName(name)
	if !ok {
		return worlds.ErrNotFound
	}
	s.clearAsides()
	rels, err := s.entries(saved)
	if err != nil {
		return err
	}
	b, err := s.backupLocked(saved, "deleted")
	if err != nil {
		return fmt.Errorf("couldn't make a safety backup, so nothing was deleted: %w", err)
	}
	a, err := s.setAside(rels, saved, b)
	if err != nil {
		return err
	}
	a.drop()
	return nil
}

// Rename gives a world a new name: its folder or files, and its backups.
// Worlds a game lists or lays out itself can't be renamed here.
func (s *store) Rename(name, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.formatError(); err != nil {
		return err
	}
	if s.f.List != nil || s.f.Files != nil {
		return game.ErrCantRename
	}
	if msg := s.NameProblem(newName); msg != "" {
		return errors.New(msg)
	}
	saved, ok := s.liveName(name)
	if !ok {
		return worlds.ErrNotFound
	}
	if newName == saved {
		return nil
	}
	// A name that differs only in letter case is the same world where case
	// doesn't count; then the rename only changes the case.
	if other, taken := s.liveName(newName); taken && other != saved {
		return fmt.Errorf("there's already a world called %q", newName)
	}
	s.clearAsides()
	rels, err := s.entries(saved)
	if err != nil {
		return err
	}
	// Nothing already under the new name may be overwritten, not even a
	// file left over from another world (a lone backup file of a game).
	for _, rel := range s.otherNames(saved) {
		t := newName + strings.TrimPrefix(rel, saved)
		st, err := os.Lstat(filepath.Join(s.dir, t))
		if err != nil {
			continue
		}
		if src, serr := os.Lstat(filepath.Join(s.dir, rel)); serr != nil || !os.SameFile(st, src) {
			return fmt.Errorf("%s is already in the worlds folder", t)
		}
	}
	var targets []string
	for _, rel := range rels {
		targets = append(targets, newName+strings.TrimPrefix(rel, saved)) // the folder, or the file with its extension
	}
	b, err := s.backupLocked(saved, "before-rename")
	if err != nil {
		return fmt.Errorf("couldn't back up the world first, so it wasn't renamed: %w", err)
	}
	// Two steps, through the hidden folder, so a change of letter case
	// alone works on Windows and macOS too, and a file that can't move
	// leaves everything as it was.
	a, err := s.setAside(rels, saved, b)
	if err != nil {
		return err
	}
	var moves [][2]string
	for i, mv := range a.moves {
		to := filepath.Join(s.dir, targets[i])
		if err := renameFile(mv[1], to); err != nil {
			// Not placeBack: these files are the world itself, not copies,
			// so one that can't go back stays under the new name rather
			// than being deleted. undo then fails on it and says so.
			for j := len(moves) - 1; j >= 0; j-- {
				renameFile(moves[j][1], moves[j][0])
			}
			if uerr := a.undo(); uerr != nil {
				return fmt.Errorf("couldn't rename the world (%v), and putting it back failed too (%v). Restore it from its %q backup.", err, uerr, b.Label)
			}
			s.dropBackup(saved, b.ID)
			return fmt.Errorf("couldn't rename the world, so it was left as it was: %w", err)
		}
		moves = append(moves, [2]string{mv[1], to})
	}
	a.drop()
	s.moveBackups(saved, newName)
	return nil
}

// otherNames lists every path a world called name could have in Dir: its
// folder, or its file for each extension.
func (s *store) otherNames(name string) []string {
	if s.f.Kind != KindFiles {
		return []string{name}
	}
	var out []string
	for _, ext := range s.f.Ext {
		out = append(out, name+ext)
	}
	return out
}

// moveBackups moves a renamed world's backups along with it. Backups
// already kept under the new name (a deleted world's) stay too.
func (s *store) moveBackups(from, to string) {
	oldDir, newDir := filepath.Join(s.backups, from), filepath.Join(s.backups, to)
	if !exists(oldDir) || os.Rename(oldDir, newDir) == nil {
		return
	}
	// Where letter case doesn't count, "Forest" and "forest" are one folder.
	if a, err := os.Stat(oldDir); err == nil {
		if b, err := os.Stat(newDir); err == nil && os.SameFile(a, b) {
			return
		}
	}
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		return
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return
	}
	for _, e := range entries {
		if exists(filepath.Join(newDir, e.Name())) {
			continue
		}
		os.Rename(filepath.Join(oldDir, e.Name()), filepath.Join(newDir, e.Name()))
	}
	os.Remove(oldDir) // only if it is empty now
}

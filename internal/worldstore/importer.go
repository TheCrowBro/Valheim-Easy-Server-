package worldstore

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/worlds"
)

// uploadDepth is how many folders deep Scan looks for worlds in an upload,
// and in each zip in it.
const uploadDepth = 4

// candidateTTL is how long the store remembers where a candidate's files
// are. Uploads are cleaned after a couple of hours anyway.
const candidateTTL = 24 * time.Hour

// located is where a candidate's files are, which worlds.Candidate can't
// hold for a package other than worlds.
type located struct {
	root  string    // the folder it was found in
	dir   string    // "folder": the world's folder
	files []srcFile // "files": the world's files
	added time.Time
}

type srcFile struct {
	path string
	ext  string // which of Format.Ext it is, spelled as Format.Ext spells it
}

// found is a candidate and where it is. ok is false for one that can't be
// imported (a zip that couldn't be opened).
type found struct {
	c   worlds.Candidate
	loc located
	ok  bool
}

// Scan looks for worlds in an upload: in the files as they are, and in each
// zip in it separately (each unpacked into its own folder in staging), so
// two copies of one world never mix. A zip that is itself a world file
// (Format.Ext holding ".zip") is kept as it is. A zip with an unsafe path,
// or one that isn't a zip, comes back as a candidate with a Problem.
func (s *store) Scan(dir string) []worlds.Candidate {
	if s.formatError() != nil {
		return nil
	}
	dir = absPath(dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	skip := map[string]bool{}
	s.cmu.Lock()
	for _, d := range s.unpacked {
		skip[d] = true // an earlier Scan's unpacked zips, if dir holds staging
	}
	s.cmu.Unlock()
	var out []found
	type root struct{ dir, name string }
	var roots []root
	for _, z := range s.zipsIn(dir, skip) {
		base := strings.TrimSuffix(filepath.Base(z), filepath.Ext(z))
		if s.isWorldFile(filepath.Base(z)) && !s.holdsWorlds(z) {
			continue // a world saved as a zip: offered as it is, below
		}
		skip[z] = true
		unpacked, err := s.unpackOnce(z)
		if err != nil {
			out = append(out, found{c: worlds.Candidate{
				Name: base, Format: s.f.Kind,
				Problem: fmt.Sprintf("Couldn't open %s: %v.", filepath.Base(z), err),
			}})
			continue
		}
		skip[unpacked] = true
		roots = append(roots, root{unpacked, base})
	}
	out = append(out, s.detect(dir, uploadDepth, "", skip, false)...)
	for _, r := range roots {
		out = append(out, s.detect(r.dir, uploadDepth, r.name, nil, false)...)
	}
	s.check(out)
	return s.register("u", out)
}

// zipsIn lists the zip files in an upload, uploadDepth folders deep.
func (s *store) zipsIn(dir string, skip map[string]bool) []string {
	var out []string
	var visit func(d string, depth int)
	visit = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			switch {
			case junk(e.Name()) || skip[p]:
			case e.IsDir():
				if depth < uploadDepth {
					visit(p, depth+1)
				}
			case e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".zip"):
				out = append(out, p)
			}
		}
	}
	visit(dir, 0)
	return out
}

// isWorldFile reports whether a file name is one of a "files" world's.
func (s *store) isWorldFile(name string) bool {
	_, _, ok := s.splitExt(name)
	return ok
}

// splitExt splits a world file's name into the world's name and which of
// Format.Ext it ends with (any letter case; the longest match wins, so
// "Forest.wld.bak" is Forest's ".wld.bak").
func (s *store) splitExt(name string) (base, ext string, ok bool) {
	if s.f.Kind != KindFiles {
		return "", "", false
	}
	for _, e := range s.f.Ext {
		if e != "" && len(name) > len(e) && strings.EqualFold(name[len(name)-len(e):], e) && len(e) > len(ext) {
			base, ext, ok = name[:len(name)-len(e)], e, true
		}
	}
	return base, ext, ok
}

// holdsWorlds reports whether a zip is an archive of world files (to be
// unpacked) rather than a world saved as a zip. A zip that can't be read
// counts as an archive, so unpacking it says what is wrong.
func (s *store) holdsWorlds(zipPath string) bool {
	zr, err := openZip(zipPath)
	if err != nil {
		return true
	}
	defer zr.Close()
	for _, f := range zr.File {
		if s.isWorldFile(path.Base(f.Name)) {
			return true
		}
	}
	return false
}

// unpackOnce unpacks a zip into a new folder in staging, or returns the
// folder an earlier Scan unpacked the same zip into.
func (s *store) unpackOnce(zipPath string) (string, error) {
	st, err := os.Stat(zipPath)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s|%d|%d", zipPath, st.Size(), st.ModTime().UnixNano())
	s.cmu.Lock()
	dir, ok := s.unpacked[key]
	s.cmu.Unlock()
	if ok && exists(dir) {
		return dir, nil
	}
	if s.staging == "" {
		return "", errors.New("this game's upload folder isn't set")
	}
	if err := os.MkdirAll(s.staging, 0o755); err != nil {
		return "", err
	}
	dir, err = os.MkdirTemp(s.staging, "unzipped-")
	if err != nil {
		return "", err
	}
	if err := unzip(zipPath, dir, worlds.MaxUploadBytes); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	s.cmu.Lock()
	s.unpacked[key] = dir
	s.cmu.Unlock()
	return dir, nil
}

// detect finds worlds under root, looking maxDepth folders deep. rootName
// names a world made of root's own files (a zip of a world's files without
// their folder: the zip's name). For local, root is a saves folder: it is
// never a world itself.
func (s *store) detect(root string, maxDepth int, rootName string, skip map[string]bool, local bool) []found {
	if s.f.Detect != nil {
		return s.detectWith(root, rootName, local)
	}
	var out []found
	var visit func(dir string, depth int)
	visit = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		switch s.f.Kind {
		case KindFolder:
			if !(local && dir == root) && holdsFiles(dir, entries, skip) {
				name := filepath.Base(dir)
				if dir == root {
					name = rootName
				}
				out = append(out, s.folderFound(root, dir, name))
				return // a world's folders are part of it
			}
		case KindFiles:
			out = append(out, s.filesFound(root, dir, entries, skip)...)
		}
		if depth >= maxDepth {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() && !junk(e.Name()) && !skip[p] {
				visit(p, depth+1)
			}
		}
	}
	visit(root, 0)
	return out
}

// holdsFiles reports whether a folder holds a file of its own, which makes
// it a world in the "folder" layout. Zips Scan unpacked separately don't
// count: they are uploads, not part of a world.
func holdsFiles(dir string, entries []fs.DirEntry, skip map[string]bool) bool {
	for _, e := range entries {
		if e.Type().IsRegular() && !junk(e.Name()) && !skip[filepath.Join(dir, e.Name())] {
			return true
		}
	}
	return false
}

func (s *store) folderFound(root, dir, name string) found {
	size, mod, _ := treeInfo(dir)
	return found{
		c:   worlds.Candidate{Name: name, Format: s.f.Kind, Size: size, Modified: mod, Renamable: true},
		loc: located{root: root, dir: dir},
		ok:  true,
	}
}

// filesFound finds the "files" worlds among a folder's files: each file
// ending with the first extension, with its siblings for the others.
func (s *store) filesFound(root, dir string, entries []fs.DirEntry, skip map[string]bool) []found {
	var out []found
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || junk(n) || skip[filepath.Join(dir, n)] {
			continue
		}
		base, ext, ok := s.splitExt(n)
		if !ok || !strings.EqualFold(ext, s.f.Ext[0]) || base == "" {
			continue
		}
		if f, ok := s.filesOf(root, dir, base, entries); ok {
			out = append(out, f)
		}
	}
	return out
}

// filesOf gathers a "files" world called base from a folder's entries.
func (s *store) filesOf(root, dir, base string, entries []fs.DirEntry) (found, bool) {
	f := found{
		c:   worlds.Candidate{Name: base, Format: s.f.Kind, Renamable: true},
		loc: located{root: root},
		ok:  true,
	}
	for _, ext := range s.f.Ext {
		pick := ""
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			if e.Name() == base+ext {
				pick = e.Name()
				break // the exact spelling wins over one in another case
			}
			if pick == "" && strings.EqualFold(e.Name(), base+ext) {
				pick = e.Name()
			}
		}
		if pick == "" {
			continue
		}
		p := filepath.Join(dir, pick)
		f.loc.files = append(f.loc.files, srcFile{path: p, ext: ext})
		if st, err := os.Stat(p); err == nil {
			f.c.Size += st.Size()
			if st.ModTime().After(f.c.Modified) {
				f.c.Modified = st.ModTime()
			}
		}
	}
	return f, len(f.loc.files) > 0
}

// detectWith asks the game's Detect for the worlds under root. Each
// candidate's ID says where it is (see Format); one whose ID doesn't point
// at a world inside root is dropped, unless the game marked a Problem.
func (s *store) detectWith(root, rootName string, local bool) []found {
	var out []found
	for _, c := range s.f.Detect(root) {
		f := found{c: c}
		f.c.ID = ""
		if f.c.Format == "" {
			f.c.Format = s.f.Kind
		}
		p, ok := within(root, c.ID)
		if ok && local && p == root {
			continue // a saves folder isn't a world itself
		}
		if ok {
			switch st, err := os.Stat(p); {
			case err != nil:
			case s.f.Kind == KindFolder && st.IsDir():
				f.loc, f.ok = located{root: root, dir: p}, true
				if f.c.Name == "" && p != root {
					f.c.Name = filepath.Base(p)
				} else if f.c.Name == "" {
					f.c.Name = rootName
				}
				if f.c.Size == 0 && f.c.Modified.IsZero() {
					f.c.Size, f.c.Modified, _ = treeInfo(p)
				}
			case s.f.Kind == KindFiles && st.Mode().IsRegular():
				if base, _, isWorld := s.splitExt(filepath.Base(p)); isWorld {
					entries, _ := os.ReadDir(filepath.Dir(p))
					if g, gok := s.filesOf(root, filepath.Dir(p), base, entries); gok {
						f.loc, f.ok = g.loc, true
						if f.c.Name == "" {
							f.c.Name = base
						}
						if f.c.Size == 0 && f.c.Modified.IsZero() {
							f.c.Size, f.c.Modified = g.c.Size, g.c.Modified
						}
					}
				}
			}
		}
		if f.ok || f.c.Problem != "" {
			out = append(out, f)
		}
	}
	return out
}

// within resolves a path a game's Detect gave, relative to root with
// forward slashes, refusing any that leaves root.
func within(root, rel string) (string, bool) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	if rel == "" || rel == "." {
		return root, true
	}
	clean, ok := safeRel(rel)
	if !ok {
		return "", false
	}
	return filepath.Join(root, clean), true
}

// check compares candidates with the live worlds: whether the name is
// taken (and by what), and whether a name that can't change can be used.
// It also marks the copies superseded by a better copy of the same world.
func (s *store) check(list []found) {
	best := map[string]int{}
	better := func(a, b worlds.Candidate) bool { // is a a better copy than b?
		if (a.Problem == "") != (b.Problem == "") {
			return a.Problem == ""
		}
		return a.Modified.After(b.Modified)
	}
	for i, f := range list {
		if f.c.Name == "" {
			continue
		}
		key := strings.ToLower(f.c.Name)
		if j, seen := best[key]; !seen || better(f.c, list[j].c) {
			best[key] = i
		}
	}
	taken := map[string]bool{}
	for i := range list {
		c := &list[i].c
		if j, ok := best[strings.ToLower(c.Name)]; ok && j != i {
			c.Superseded = true
		}
		if c.Name != "" && !c.Superseded {
			taken[strings.ToLower(c.Name)] = true
		}
	}
	for i := range list {
		c := &list[i].c
		if c.Problem == "" && !c.Renamable {
			if c.Name == "" {
				c.Problem = "This world has no name."
			} else if msg := s.NameProblem(c.Name); msg != "" {
				c.Problem = msg
			}
		}
		saved := ""
		if c.Name != "" {
			saved, c.Exists = s.liveName(c.Name)
		}
		if !c.Exists {
			continue
		}
		if l, ok := s.liveInfo(saved); ok {
			c.ServerModified, c.ServerSize = l.Modified, l.Size
		}
		if c.Renamable && !c.Superseded {
			c.FreeName = s.freeName(c.Name, taken)
			taken[strings.ToLower(c.FreeName)] = true
		}
	}
}

// liveInfo is one live world's size and last save.
func (s *store) liveInfo(name string) (Live, bool) {
	if s.f.List == nil {
		return s.info(name), true
	}
	live, err := s.f.List()
	if err != nil {
		return Live{}, false
	}
	for _, l := range live {
		if l.Name == name {
			return l, true
		}
	}
	return Live{}, false
}

// register gives candidates new IDs and remembers where each one's files
// are, for Import.
func (s *store) register(prefix string, list []found) []worlds.Candidate {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	now := time.Now()
	for id, l := range s.cands {
		if now.Sub(l.added) > candidateTTL {
			delete(s.cands, id)
		}
	}
	out := make([]worlds.Candidate, 0, len(list))
	for _, f := range list {
		s.seq++
		f.c.ID = prefix + strconv.Itoa(s.seq)
		if f.ok {
			f.loc.added = now
			s.cands[f.c.ID] = f.loc
		}
		out = append(out, f.c)
	}
	return out
}

// forgetUnpacked stops reusing a folder a zip was unpacked into, so the
// next Scan of that zip unpacks it afresh.
func (s *store) forgetUnpacked(dir string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	for key, d := range s.unpacked {
		if d == dir {
			delete(s.unpacked, key)
		}
	}
}

func (s *store) lookup(id string) (located, bool) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	l, ok := s.cands[id]
	return l, ok
}

// Local finds worlds the owner's own copy of the game keeps on this
// computer (Format.LocalIn), skipping the server's own folder. They are
// only ever copied from there, never moved.
func (s *store) Local() []worlds.Candidate {
	if s.f.LocalIn == nil || s.formatError() != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	var out []found
	seen := map[string]bool{}
	for _, folder := range s.f.LocalIn(home) {
		if folder == "" {
			continue
		}
		dir := absPath(folder)
		if strings.EqualFold(dir, s.dir) || seen[strings.ToLower(dir)] {
			continue
		}
		seen[strings.ToLower(dir)] = true
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		list := s.detect(dir, 1, "", nil, true)
		for i := range list {
			list[i].c.Source = dir
		}
		out = append(out, list...)
	}
	s.check(out)
	return s.register("local-", out)
}

// CleanStaging removes uploads (and zips unpacked from them) older than
// maxAge, and forgets candidates whose files are gone.
func (s *store) CleanStaging(maxAge time.Duration) {
	// An import may be copying from an upload right now; then cleaning
	// waits for the next call rather than holding up whoever called.
	if s.staging != "" && s.mu.TryLock() {
		if entries, err := os.ReadDir(s.staging); err == nil {
			for _, e := range entries {
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
					os.RemoveAll(filepath.Join(s.staging, e.Name()))
				}
			}
		}
		s.mu.Unlock()
	}
	s.cmu.Lock()
	defer s.cmu.Unlock()
	for key, dir := range s.unpacked {
		if !exists(dir) {
			delete(s.unpacked, key)
		}
	}
	for id, l := range s.cands {
		if !exists(l.root) {
			delete(s.cands, id)
		}
	}
}

// Import copies (or, with move, moves) the chosen candidates into Dir.
// Worlds that get replaced are backed up first, and put back if the new
// copy can't be placed. busy reports whether a world is in use by the
// running server. Everything is checked before anything is changed.
func (s *store) Import(cands []worlds.Candidate, choices []worlds.Choice, busy func(string) bool, move bool) ([]worlds.Placed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.formatError(); err != nil {
		return nil, err
	}
	expired := errors.New("That upload has expired. Please upload the world again.")
	byID := map[string]worlds.Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	type job struct {
		c       worlds.Candidate
		loc     located
		name    string
		saved   string // the name the replaced world is saved under
		replace bool
	}
	var jobs []job
	taken := map[string]bool{}
	for _, ch := range choices {
		if ch.Action == "skip" {
			continue
		}
		c, ok := byID[ch.ID]
		if !ok {
			return nil, expired
		}
		if c.Problem != "" {
			return nil, errors.New(c.Problem)
		}
		loc, ok := s.lookup(c.ID)
		if !ok {
			return nil, expired
		}
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			name = c.Name
		}
		if !c.Renamable && name != c.Name {
			return nil, fmt.Errorf("This world keeps its own name (%s).", c.Name)
		}
		if msg := s.NameProblem(name); msg != "" {
			return nil, errors.New(msg)
		}
		key := strings.ToLower(name)
		if taken[key] {
			return nil, fmt.Errorf("Two worlds would both be called %q.", name)
		}
		taken[key] = true
		saved, exists := s.liveName(name)
		if exists && ch.Action != "replace" {
			return nil, fmt.Errorf("There's already a world called %q. Choose Replace or a different name.", name)
		}
		if exists && busy != nil && busy(saved) {
			return nil, fmt.Errorf("%q is running on the server right now. Stop the server before replacing it.", name)
		}
		if !exists {
			saved = name
		}
		jobs = append(jobs, job{c, loc, name, saved, exists})
	}
	s.clearAsides()
	var done []worlds.Placed
	for _, j := range jobs {
		if err := s.importOne(j.c, j.loc, j.name, j.saved, j.replace, move); err != nil {
			return done, err
		}
		p := worlds.Placed{Name: j.name, Replaced: j.replace}
		if j.replace && j.saved != j.name {
			p.Was = j.saved
		}
		done = append(done, p)
	}
	return done, nil
}

// importOne puts one world in place: it is first copied (or moved) into a
// hidden folder in Dir, then whatever is saved under its name is backed up
// and set aside, then the copy is moved into place in one step, and
// Format.Place runs. Any failure undoes all of it.
func (s *store) importOne(c worlds.Candidate, loc located, name, saved string, replace, move bool) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.dir, incomingPrefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	moved, err := stageCandidate(loc, name, stage, s.f.Kind, move)
	// putBack returns moved upload files, so the upload can be tried again.
	putBack := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			os.Rename(moved[i][1], moved[i][0])
		}
	}
	if err != nil {
		putBack()
		return fmt.Errorf("couldn't import %q: %w", name, err)
	}
	// Anything saved under the name now, even files left over from a
	// world that is gone, is kept in a backup and set aside.
	rels, err := s.entries(saved)
	if err != nil {
		putBack()
		return err
	}
	var safety worlds.Backup
	if len(rels) > 0 {
		safety, err = s.backupLocked(saved, "before-replace")
		if err != nil {
			putBack()
			return fmt.Errorf("couldn't back up %q before replacing it, so it was left alone: %w", saved, err)
		}
	}
	a, err := s.setAside(rels, saved, safety)
	if err != nil {
		putBack()
		return err
	}
	placed, err := mergeMove(stage, s.dir)
	if err == nil && s.f.Place != nil {
		err = s.f.Place(c, name)
	}
	if err == nil {
		a.drop()
		if len(moved) > 0 {
			s.forgetUnpacked(loc.root) // that unpacked zip is missing a world now
		}
		return nil
	}
	placeBack(placed)
	putBack()
	if uerr := a.undo(); uerr != nil {
		return fmt.Errorf("couldn't import %q (%v), and putting the server's old copy back failed too (%v). Restore it from its %q backup.", name, err, uerr, safety.Label)
	}
	s.dropBackup(saved, safety.ID)
	if replace {
		return fmt.Errorf("couldn't import %q, so the server's copy was left as it was: %w", name, err)
	}
	return fmt.Errorf("couldn't import %q: %w", name, err)
}

// stageCandidate copies (or moves) a candidate's files into stage, laid out
// as the world called name is in Dir. It returns what it moved (from, to),
// so a failed import can give the upload its files back. A candidate whose
// files are the scanned folder itself is always copied, so the upload's
// own folder never moves.
func stageCandidate(loc located, name, stage, kind string, move bool) ([][2]string, error) {
	var moved [][2]string
	put := func(src, dst string, isDir bool) error {
		if move && src != loc.root {
			if err := os.Rename(src, dst); err == nil {
				moved = append(moved, [2]string{src, dst})
				return nil
			}
			// Another drive: copy, and leave the upload to be cleaned.
		}
		if isDir {
			return copyDir(src, dst)
		}
		return copyFile(src, dst)
	}
	gone := errors.New("its files aren't there any more. Please add the world again")
	switch kind {
	case KindFolder:
		if loc.dir == "" || !exists(loc.dir) {
			return nil, gone
		}
		return moved, put(loc.dir, filepath.Join(stage, name), true)
	case KindFiles:
		if len(loc.files) == 0 {
			return nil, gone
		}
		for _, f := range loc.files {
			if !exists(f.path) {
				return moved, gone
			}
		}
		for _, f := range loc.files {
			if err := put(f.path, filepath.Join(stage, name+f.ext), false); err != nil {
				return moved, err
			}
		}
	}
	return moved, nil
}

// safeRel cleans a relative path from an upload or zip, refusing anything
// that could reach outside the folder it is unpacked into: "..", absolute
// paths, drive letters, and names Windows keeps for devices.
func safeRel(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, ":\x00") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false
	}
	return filepath.FromSlash(clean), true
}

// openZip opens a zip for reading. Go can be set (GODEBUG
// zipinsecurepath=0) to refuse a zip with an unsafe path outright; the
// store checks every path itself, so such a zip is opened the same way
// and refused with the path named.
func openZip(zipPath string) (*zip.ReadCloser, error) {
	zr, err := zip.OpenReader(zipPath)
	if errors.Is(err, zip.ErrInsecurePath) && zr != nil {
		return zr, nil
	}
	return zr, err
}

// unzip unpacks a zip into dest, stopping after limit bytes (0 = no limit).
// Every path is checked before anything is written, so a zip with an
// unsafe path writes nothing at all.
func unzip(zipPath, dest string, limit int64) error {
	zr, err := openZip(zipPath)
	if err != nil {
		return errors.New("it isn't a valid zip file")
	}
	defer zr.Close()
	type item struct {
		f   *zip.File
		rel string
	}
	var items []item
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "__MACOSX/") || path.Base(f.Name) == ".DS_Store" {
			continue
		}
		rel, ok := safeRel(f.Name)
		if !ok {
			return fmt.Errorf("it contains an unsafe path (%s)", f.Name)
		}
		items = append(items, item{f, rel})
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var total int64
	for _, it := range items {
		target := filepath.Join(dest, it.rel)
		if it.f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !it.f.Mode().IsRegular() {
			continue // links and other special files are left out
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := it.f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		max := int64(1 << 62)
		if limit > 0 {
			max = limit - total
		}
		n, err := io.Copy(out, io.LimitReader(rc, max+1))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		total += n
		if limit > 0 && total > limit {
			return errors.New("it is too big")
		}
		if !it.f.Modified.IsZero() {
			os.Chtimes(target, it.f.Modified, it.f.Modified) // keeps "last saved" meaningful
		}
	}
	return nil
}

// copyFile copies src to dst, creating folders as needed and keeping the
// file's time.
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
	if st, err := in.Stat(); err == nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

// copyDir copies a folder tree. Links and special files are left out, so
// a link in an upload can't pull in files from elsewhere.
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
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(p, target)
		}
		return nil
	})
}

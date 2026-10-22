package worlds

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
)

// Candidate is a world found in an upload or in a folder on this computer,
// waiting to be imported.
type Candidate struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"` // suggested name on the server
	Format    string    `json:"format"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Seed      string    `json:"seed,omitempty"`
	Exists    bool      `json:"exists"`    // the server already has a world with this name
	Renamable bool      `json:"renamable"` // false for 1.0 worlds, which keep their name
	Note      string    `json:"note,omitempty"`
	Problem   string    `json:"problem,omitempty"` // can't be imported as is
	Source    string    `json:"source,omitempty"`

	dir     string // 1.0 world: its folder
	fwlPath string // classic world: the .fwl file
	dbPath  string // classic world: the .db file, if any
}

// Detect finds worlds under root, looking maxDepth folders deep. Valheim's
// own backup copies are offered too, named after the world they belong to.
func Detect(root string, maxDepth int) []Candidate {
	var out []Candidate
	var visit func(dir string, depth int)
	visit = func(dir string, depth int) {
		if size, mod, ok := folderWorldInfo(dir); ok {
			c := Candidate{Format: FormatFolder, Size: size, Modified: mod, dir: dir}
			meta, haveMeta := folderMeta(dir)
			if haveMeta {
				c.Seed = meta.SeedName
			}
			name := filepath.Base(dir)
			if m := reBackupName.FindStringSubmatch(name); m != nil {
				name = m[1]
				c.Note = "This is one of Valheim's backup copies of the world."
			}
			if dir == root { // loose files without their folder
				name = ""
			}
			if haveMeta && meta.Name != "" {
				name = meta.Name // the name stored inside the world wins
			}
			c.Name = name
			c.Renamable = name == ""
			out = append(out, c)
			return // a world folder doesn't contain other worlds
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() {
				if depth < maxDepth && n != "__MACOSX" {
					visit(filepath.Join(dir, n), depth+1)
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(n), ".fwl") {
				continue
			}
			base := n[:len(n)-4]
			c := Candidate{Format: FormatClassic, Name: base, Renamable: true, fwlPath: filepath.Join(dir, n)}
			for _, ext := range []string{".db", ".DB"} {
				if p := filepath.Join(dir, base+ext); fileExists(p) {
					c.dbPath = p
					break
				}
			}
			for _, p := range []string{c.fwlPath, c.dbPath} {
				if st, err := os.Stat(p); p != "" && err == nil {
					c.Size += st.Size()
					if st.ModTime().After(c.Modified) {
						c.Modified = st.ModTime()
					}
				}
			}
			if m := reBackupName.FindStringSubmatch(base); m != nil {
				c.Name = m[1]
				c.Note = "This is one of Valheim's backup copies of the world."
			}
			if data, err := os.ReadFile(c.fwlPath); err == nil {
				if meta, err := ParseMeta(data); err == nil {
					c.Seed = meta.SeedName
				} else {
					c.Problem = "This .fwl file doesn't look like a Valheim world."
				}
			}
			if c.dbPath == "" && c.Problem == "" {
				c.Note = "There's no .db file, so Valheim will create this world fresh from its seed."
			}
			out = append(out, c)
		}
	}
	visit(root, 0)
	for i := range out {
		out[i].ID = strconv.Itoa(i + 1)
	}
	return out
}

// Check fills in Exists and Problem for the server's current worlds.
func (l *Library) Check(cands []Candidate) []Candidate {
	for i := range cands {
		c := &cands[i]
		c.Exists = c.Name != "" && l.exists(c.Name)
		if c.Problem == "" && c.Name != "" {
			if msg := config.WorldNameProblem(c.Name); msg != "" && !c.Renamable {
				c.Problem = msg
			}
		}
	}
	return cands
}

// Choice says what to do with one candidate.
type Choice struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "import", "replace" or "skip"
	Name   string `json:"name"`   // final name; empty keeps the suggestion
}

// Import copies (or, with move, moves) the chosen candidates into the
// worlds folder. Worlds that get replaced are backed up first. busy reports
// whether a world is in use by the running server.
func (l *Library) Import(cands []Candidate, choices []Choice, busy func(string) bool, move bool) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	byID := map[string]Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	type job struct {
		c       Candidate
		name    string
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
			return nil, errors.New("that upload has expired; please upload the world again")
		}
		if c.Problem != "" {
			return nil, errors.New(c.Problem)
		}
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			name = c.Name
		}
		if !c.Renamable && name != c.Name {
			return nil, fmt.Errorf("Valheim 1.0 worlds keep their own name (%s).", c.Name)
		}
		if msg := config.WorldNameProblem(name); msg != "" {
			return nil, errors.New(msg)
		}
		key := strings.ToLower(name)
		if taken[key] {
			return nil, fmt.Errorf("two worlds would both be called %q", name)
		}
		taken[key] = true
		exists := l.exists(name)
		if exists && ch.Action != "replace" {
			return nil, fmt.Errorf("there's already a world called %q. Choose Replace or a different name.", name)
		}
		if exists && busy(name) {
			return nil, fmt.Errorf("%q is running on the server right now. Stop the server before replacing it.", name)
		}
		jobs = append(jobs, job{c, name, exists})
	}
	var done []string
	for _, j := range jobs {
		if j.replace {
			if _, err := l.backupLocked(j.name, "before-replace"); err != nil {
				return done, fmt.Errorf("couldn't back up %q before replacing it, so it was left alone: %w", j.name, err)
			}
			if err := l.removeLive(j.name); err != nil {
				return done, err
			}
		}
		if err := place(j.c, j.name, l.Worlds, move); err != nil {
			return done, fmt.Errorf("couldn't import %q: %w", j.name, err)
		}
		done = append(done, j.name)
	}
	return done, nil
}

// place puts a candidate world into worldsDir under name.
func place(c Candidate, name, worldsDir string, move bool) error {
	if err := os.MkdirAll(worldsDir, 0o755); err != nil {
		return err
	}
	switch c.Format {
	case FormatFolder:
		dst := filepath.Join(worldsDir, name)
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("a folder called %q is already there", name)
		}
		if move {
			return moveOrCopy(c.dir, dst, true)
		}
		return copyDir(c.dir, dst)
	default:
		dst := filepath.Join(worldsDir, name)
		data, err := os.ReadFile(c.fwlPath)
		if err != nil {
			return err
		}
		if meta, err := ParseMeta(data); err == nil && meta.Name != name {
			if data, err = RenameMeta(data, name); err != nil {
				return err
			}
		}
		if c.dbPath != "" {
			if move {
				err = moveOrCopy(c.dbPath, dst+".db", false)
			} else {
				err = copyFile(c.dbPath, dst+".db")
			}
			if err != nil {
				return err
			}
		}
		return config.WriteFileAtomic(dst+".fwl", data, 0o644)
	}
}

// Upload is a set of uploaded files waiting to be imported.
type Upload struct {
	ID         string
	Dir        string
	Content    string
	Created    time.Time
	Candidates []Candidate
}

// MaxUploadBytes caps one upload (worlds are rarely more than a few GB).
const MaxUploadBytes = 16 << 30

// NewUpload creates an empty upload area.
func (l *Library) NewUpload() (*Upload, error) {
	if err := os.MkdirAll(l.Staging, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(l.Staging, "upload-")
	if err != nil {
		return nil, err
	}
	u := &Upload{ID: filepath.Base(dir), Dir: dir, Content: filepath.Join(dir, "content"), Created: time.Now()}
	return u, os.MkdirAll(u.Content, 0o755)
}

// Remove deletes the upload's files.
func (u *Upload) Remove() { os.RemoveAll(u.Dir) }

// AddFile stores one uploaded file. rel may include folders (when a whole
// folder was chosen). Zip files are unpacked.
func (u *Upload) AddFile(rel string, r io.Reader) error {
	clean, ok := safeRel(rel)
	if !ok {
		return fmt.Errorf("unsafe file name %q", rel)
	}
	if strings.EqualFold(filepath.Ext(clean), ".zip") {
		tmp, err := os.CreateTemp(u.Dir, "incoming-*.zip")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		n, err := io.Copy(tmp, io.LimitReader(r, MaxUploadBytes+1))
		tmp.Close()
		if err != nil {
			return err
		}
		if n > MaxUploadBytes {
			return errors.New("the upload is too big")
		}
		if err := extractZip(tmp.Name(), u.Content, MaxUploadBytes); err != nil {
			return fmt.Errorf("couldn't open %s: %w", filepath.Base(clean), err)
		}
		return nil
	}
	dst := filepath.Join(u.Content, clean)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxUploadBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxUploadBytes {
		err = errors.New("the upload is too big")
	}
	return err
}

// Scan looks for worlds in the upload.
func (u *Upload) Scan(l *Library) []Candidate {
	u.Candidates = l.Check(Detect(u.Content, 4))
	return u.Candidates
}

// CleanUploads removes uploads older than maxAge.
func (l *Library) CleanUploads(maxAge time.Duration) {
	entries, err := os.ReadDir(l.Staging)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			os.RemoveAll(filepath.Join(l.Staging, e.Name()))
		}
	}
}

// safeRel cleans a relative path from an upload or zip, rejecting anything
// that could escape the destination folder.
func safeRel(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" {
		return "", false
	}
	return filepath.FromSlash(clean), true
}

// extractZip unpacks a zip into dest, refusing unsafe paths and stopping
// after limit bytes (0 = no limit).
func extractZip(zipPath, dest string, limit int64) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("it isn't a valid zip file")
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasSuffix(f.Name, ".DS_Store") {
			continue
		}
		rel, ok := safeRel(f.Name)
		if !ok {
			return fmt.Errorf("it contains an unsafe path (%s)", f.Name)
		}
		target := filepath.Join(dest, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue // skip links and other special files
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
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
		os.Chtimes(target, f.Modified, f.Modified)
	}
	return nil
}

// Source is a folder on this computer where Valheim keeps worlds.
type Source struct {
	Label string
	Dir   string
}

// LocalCandidates finds worlds in folders on this computer (the Valheim
// game's own saves), skipping the server's own folder.
func (l *Library) LocalCandidates(sources []Source) []Candidate {
	var out []Candidate
	serverDir, _ := filepath.Abs(l.Worlds)
	for _, s := range sources {
		dir, _ := filepath.Abs(s.Dir)
		if strings.EqualFold(dir, serverDir) {
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		for _, c := range Detect(dir, 1) {
			if c.dir == dir { // the folder itself isn't a world
				continue
			}
			c.Source = s.Label
			out = append(out, c)
		}
	}
	for i := range out {
		out[i].ID = "local-" + strconv.Itoa(i+1)
	}
	return l.Check(out)
}

// GameSaveSources lists where the Valheim game keeps worlds for this user.
func GameSaveSources(home, steamDir string, windows bool) []Source {
	var base string
	if windows {
		base = filepath.Join(home, "AppData", "LocalLow", "IronGate", "Valheim")
	} else {
		base = filepath.Join(home, ".config", "unity3d", "IronGate", "Valheim")
	}
	sources := []Source{
		{"Valheim on this computer", filepath.Join(base, "worlds_local")},
		{"Valheim on this computer (older saves)", filepath.Join(base, "worlds")},
	}
	if steamDir != "" {
		users, _ := filepath.Glob(filepath.Join(steamDir, "userdata", "*", "892970", "remote"))
		for _, u := range users {
			for _, sub := range []string{"worlds", "worlds_local"} {
				if st, err := os.Stat(filepath.Join(u, sub)); err == nil && st.IsDir() {
					sources = append(sources, Source{"Steam Cloud saves", filepath.Join(u, sub)})
				}
			}
		}
	}
	return sources
}

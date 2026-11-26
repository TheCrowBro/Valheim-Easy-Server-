package servers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
)

// Status says what Load found in servers.json itself.
type Status int

const (
	OK      Status = iota // a list this build can use and change
	Missing               // no file
	Damaged               // the file can't be read, or isn't a list (invalid JSON, a value of the wrong type, no schema number)
	Newer                 // written by a newer version of the app (schema > Schema): safe mode, never rewritten
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Missing:
		return "missing"
	case Damaged:
		return "damaged"
	case Newer:
		return "newer"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// ErrNewer is in the error for a list written by a newer version of the
// app. Its meaning can't be known here, so this build doesn't use or
// change it: the manager runs only the legacy game (safe mode, spec 8.3),
// and after the automatic rollback the newer build finds its list intact
// when it is installed again.
var ErrNewer = errors.New("the list of games was saved by a newer version of the app")

// writeMu keeps the writes of this process one at a time, so two changes
// made at once (a star and a setting) can't lose one another.
var writeMu sync.Mutex

// copyRead is one copy of the list (servers.json or its backup) as read.
type copyRead struct {
	raw    []byte // the bytes read; nil when the file couldn't be read at all
	file   File
	status Status
	err    error // why it is Damaged or Newer
}

// readCopy reads and checks one copy of the list.
func readCopy(path string) copyRead {
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return copyRead{status: Missing}
	case err != nil:
		return copyRead{status: Damaged, err: err}
	}
	f, st, err := parse(raw)
	return copyRead{raw: raw, file: f, status: st, err: err}
}

// parse checks the schema number first, so a newer file is recognised
// even when its fields no longer fit this build's types, and reports
// anything this build can't read as Damaged.
func parse(raw []byte) (File, Status, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return File{}, Damaged, err
	}
	var f File
	switch {
	case head.Schema > Schema:
		// Read what fits, for showing only; it is never written back.
		_ = json.Unmarshal(raw, &f)
		return f, Newer, fmt.Errorf("%w (its version is %d; this one knows up to %d)", ErrNewer, head.Schema, Schema)
	case head.Schema < 1:
		return File{}, Damaged, errors.New("it has no version number")
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, Damaged, err
	}
	return f, OK, nil
}

// encode is how the list is written: indented, so it can be read and fixed
// by hand, with a final newline.
func encode(f File) ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// write writes one copy of the list atomically (a crash leaves the old
// file or the new one, never half of each) and private: it holds Discord
// webhooks, which let anyone post in the owner's channels (invariant I9).
func write(path string, data []byte) error {
	return config.WriteFileAtomic(path, data, 0o600)
}

// Load reads the list at path (servers.json), by spec 3.4's rules, and
// changes no file:
//
//   - OK: the list as it is; the error is nil.
//   - Missing: there is no file; the list is empty (Schema 0) and the error
//     nil. Open applies 3.4's rules for this (backup, rebuild, first start).
//   - Damaged: the file can't be used, so the list is its backup
//     (path + BackupSuffix) and the error is nil; when the backup can't be
//     used either, the error says why (safe mode).
//   - Newer: written by a newer version; the error wraps ErrNewer, and the
//     list is what this build could read of it, for showing only (safe
//     mode).
//
// So a non-nil error always means safe mode: run only the legacy game and
// never write the file.
func Load(path string) (File, Status, error) {
	cur := readCopy(path)
	if cur.status != Damaged {
		return cur.file, cur.status, cur.err
	}
	bak := readCopy(path + BackupSuffix)
	if bak.status == OK {
		return bak.file, Damaged, nil
	}
	return File{}, Damaged, bothUnusable(path, cur, bak)
}

// bothUnusable explains why neither the list nor its backup can be used.
func bothUnusable(path string, cur, bak copyRead) error {
	name := filepath.Base(path)
	switch bak.status {
	case Missing:
		return fmt.Errorf("%s can't be used (%v), and it has no backup", name, cur.err)
	case Newer:
		// Wrapped, so errors.Is(err, ErrNewer) holds.
		return fmt.Errorf("%s can't be used (%v), and its backup can't be used either: %w", name, cur.err, bak.err)
	}
	return fmt.Errorf("%s can't be used (%v), and its backup can't be used either (%v)", name, cur.err, bak.err)
}

// Update changes the list at path: it reads it, calls change, keeps the
// file it read as the backup, and writes the result, one change at a time.
// It refuses unless the file reads OK, so it never writes over a list from
// a newer version, a damaged one whose backup is in use, or a missing one
// (Open handles those when the app starts). An error from change is
// returned as it is, with nothing written; so is a change that would leave
// the list unsafe for this build to read back (an id that can't be a
// folder name, an id used twice, two legacy instances). A change that
// changes nothing writes nothing, so the backup stays the previous list.
func Update(path string, change func(*File) error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	cur := readCopy(path)
	switch {
	case cur.status == Newer:
		return fmt.Errorf("not changing %s: %w", filepath.Base(path), cur.err)
	case cur.status != OK:
		why := cur.status.String()
		if cur.err != nil {
			why = fmt.Sprintf("%v (%v)", cur.status, cur.err)
		}
		return fmt.Errorf("not changing %s: it is %s", filepath.Base(path), why)
	}
	f := cur.file
	before := problems(f)
	if err := change(&f); err != nil {
		return err
	}
	if p := slices.DeleteFunc(problems(f), func(p string) bool { return slices.Contains(before, p) }); len(p) > 0 {
		return fmt.Errorf("not changing %s: %s", filepath.Base(path), p[0])
	}
	f.Schema = Schema
	data, err := encode(f)
	if err != nil {
		return err
	}
	if bytes.Equal(data, cur.raw) {
		return nil
	}
	if err := write(path+BackupSuffix, cur.raw); err != nil {
		return fmt.Errorf("couldn't keep a backup of %s: %w", filepath.Base(path), err)
	}
	return write(path, data)
}

// problems lists what would make the list unsafe for the manager: an id
// that can't be a folder name, an id used twice, more than one legacy
// instance. Load doesn't call these damage (a newer build of the same
// schema might relax a rule, and its list must not be thrown away);
// Update only refuses to add new ones.
func problems(f File) []string {
	var out []string
	seen := map[string]bool{}
	legacy := 0
	for _, in := range f.Instances {
		switch {
		case !ValidID(in.ID):
			out = append(out, fmt.Sprintf("the instance id %q can't be a folder name", in.ID))
		case seen[in.ID]:
			out = append(out, fmt.Sprintf("the instance id %q is used twice", in.ID))
		}
		seen[in.ID] = true
		if in.Legacy {
			legacy++
		}
	}
	if legacy > 1 {
		out = append(out, fmt.Sprintf("%d instances are marked legacy; only one can be", legacy))
	}
	return out
}

// Opened says what Open found and did, for the app's log and the warning
// it shows in the activity feed.
type Opened struct {
	Status     Status    // what servers.json itself was
	FromBackup bool      // the list is servers.json.bak's
	BackupTime time.Time // when that backup was written (zero if unknown)
	Kept       string    // where a damaged servers.json was moved, to look at later ("" if none)
	Created    bool      // there was no list and no sign of one: a first start, with only the legacy game
	Rebuilt    bool      // there was no usable list but signs of one (games/ or a backup): it was made again
	Found      []string  // the instances Rebuilt put back from games/ folders: their ports still need picking
	SafeMode   bool      // run only the legacy instance, and never write servers.json (Update refuses anyway)
	WriteErr   error     // the list couldn't be written back; it is used anyway, but Update refuses until the next start
}

// Warning is the warning for the activity feed, or "" when there is
// nothing to say. Safe mode has none here: the Overview explains it.
func (o Opened) Warning() string {
	when := ""
	if !o.BackupTime.IsZero() {
		when = " from " + o.BackupTime.Local().Format("2 January 2006 at 15:04")
	}
	switch {
	case o.SafeMode:
		return ""
	case o.FromBackup && o.Status == Damaged:
		return "The list of games was damaged; the app used its backup" + when + "."
	case o.FromBackup:
		return "The list of games was missing; the app used its backup" + when + "."
	case o.Rebuilt:
		// Not "settings are back to defaults": the legacy game's settings
		// live in settings.json and are untouched.
		return "The list of games was missing, so the app made it again from the games' folders. Check the stars and each game's settings."
	}
	return ""
}

// Open loads servers.json from the data folder when the app starts,
// applying all of spec 3.4's rules, and writes back what it had to make:
//
//   - OK: used as it is; nothing is written.
//   - Newer: safe mode; nothing is touched.
//   - Damaged: the backup is used when it is valid. The damaged file is
//     moved to servers.json.damaged-<stamp> and the backup is written back
//     as servers.json at once, so the next start finds a good list and a
//     change can be saved. (Waiting for the next change, as 3.4 first put
//     it, would set the damaged file aside again and warn again on every
//     start until then.) A file that can't even be read (a permission, a
//     lock) is left where it is, as it may be a good list: the backup is
//     used for this run and WriteErr says why changes can't be saved.
//     With no valid backup: safe mode, nothing touched.
//   - Missing: the backup when it is valid, written back the same way.
//     Otherwise a new list: the legacy game, starred, plus, when there are
//     signs of an earlier list, an unstarred instance for each games/<id>
//     folder whose name is a game isGame knows (default settings, no ports:
//     the manager picks them, keeping each role's default when it's free).
//
// legacyID is the legacy game's id ("" when none is registered). isGame
// says whether an id is a registered game; nil knows none. now stamps new
// instances and a damaged file's new name. A non-nil error means safe
// mode; the list returned is then for showing only.
func Open(dataDir, legacyID string, isGame func(id string) bool, now time.Time) (File, Opened, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	path := filepath.Join(dataDir, FileName)
	cur := readCopy(path)
	o := Opened{Status: cur.status}
	switch cur.status {
	case OK:
		return cur.file, o, nil
	case Newer:
		o.SafeMode = true
		return cur.file, o, cur.err
	}

	bakPath := path + BackupSuffix
	bak := readCopy(bakPath)
	if bak.status == OK {
		o.FromBackup = true
		if fi, err := os.Stat(bakPath); err == nil {
			o.BackupTime = fi.ModTime()
		}
		if cur.status == Damaged {
			if cur.raw == nil {
				// It couldn't even be read (a permission, a lock): moving
				// or replacing it could lose a good list, so leave it.
				o.WriteErr = fmt.Errorf("%s couldn't be read, so it was left as it is: %w", FileName, cur.err)
				return bak.file, o, nil
			}
			kept := path + ".damaged-" + now.Format("20060102-150405")
			if err := os.Rename(path, kept); err != nil {
				o.WriteErr = fmt.Errorf("couldn't set the damaged %s aside: %w", FileName, err)
				return bak.file, o, nil
			}
			os.Chmod(kept, 0o600) // it may hold webhooks; best effort (ignored on Windows)
			o.Kept = kept
		}
		o.WriteErr = write(path, bak.raw)
		return bak.file, o, nil
	}
	if cur.status == Damaged || bak.status == Newer {
		// Nothing usable, or only a newer version's backup, which a new
		// list would soon overwrite: change nothing and run only the
		// legacy game, as for a newer list.
		o.SafeMode = true
		if cur.status == Missing {
			return bak.file, o, fmt.Errorf("%s is missing, and its backup can't be used: %w", FileName, bak.err)
		}
		return File{}, o, bothUnusable(path, cur, bak)
	}

	// Missing, with no usable backup: make a new list.
	f := New(legacyID, now)
	found, gamesDir := scanGames(dataDir, legacyID, isGame)
	if gamesDir || bak.status != Missing {
		o.Rebuilt = true
		for _, id := range found {
			f.Instances = append(f.Instances, Instance{ID: id, Game: id, Added: now.UTC()})
		}
		o.Found = found
	} else {
		o.Created = true
	}
	data, err := encode(f)
	if err == nil {
		err = write(path, data)
	}
	o.WriteErr = err
	return f, o, nil
}

// scanGames lists the games/<id> folders whose name is a game isGame knows
// (never the legacy game, whose files never live there, invariant I1), in
// name order, and says whether games/ exists at all.
func scanGames(dataDir, legacyID string, isGame func(id string) bool) (found []string, exists bool) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "games"))
	if err != nil {
		return nil, !errors.Is(err, fs.ErrNotExist)
	}
	for _, e := range entries {
		id := e.Name()
		if e.IsDir() && id != legacyID && ValidID(id) && isGame != nil && isGame(id) {
			found = append(found, id)
		}
	}
	return found, true
}

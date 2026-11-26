// Package worldstore is the world library of every game except Valheim
// (spec 2.8). It reuses the format-independent ideas of Valheim's
// internal/worlds Library (zip backups with a fingerprint retry while the
// server saves, all-or-nothing replace with set-aside and undo, import from
// a zip or folder with safe paths) in new code, so Valheim's library never
// changes.
//
// Phase 0 stub: the exported signatures below are the contract the game
// streams build against; stream W fills in the bodies. Until then every
// call fails with errors.ErrUnsupported, and no shipped code calls any of it.
package worldstore

import (
	"errors"
	"io"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// Format is how a game keeps its worlds.
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
func New(f Format, backups, staging string) game.Worlds {
	return unsupported{}
}

// unsupported stands in until stream W writes the real store.
type unsupported struct{}

func (unsupported) List() ([]worlds.World, []worlds.Orphan, error) {
	return nil, nil, errors.ErrUnsupported
}

func (unsupported) Get(name string) (worlds.World, error) {
	return worlds.World{}, errors.ErrUnsupported
}

func (unsupported) Exists(name string) bool { return false }

// NameProblem refuses every name, so nothing can be made in a store that
// doesn't exist yet.
func (unsupported) NameProblem(name string) string { return errors.ErrUnsupported.Error() }

func (unsupported) Backup(name, reason string) (worlds.Backup, error) {
	return worlds.Backup{}, errors.ErrUnsupported
}

func (unsupported) Restore(name, backupID string) error { return errors.ErrUnsupported }

func (unsupported) Delete(name string) error { return errors.ErrUnsupported }

func (unsupported) Rename(name, newName string) error { return errors.ErrUnsupported }

func (unsupported) ExportFile(name, dir string) (string, error) {
	return "", errors.ErrUnsupported
}

func (unsupported) ExportBackup(w io.Writer, name, id string) error { return errors.ErrUnsupported }

func (unsupported) Scan(dir string) []worlds.Candidate { return nil }

func (unsupported) Local() []worlds.Candidate { return nil }

func (unsupported) Import(c []worlds.Candidate, ch []worlds.Choice, busy func(string) bool, move bool) ([]worlds.Placed, error) {
	return nil, errors.ErrUnsupported
}

func (unsupported) CleanStaging(maxAge time.Duration) {}

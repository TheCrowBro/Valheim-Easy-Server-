package valheim

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worlds"
)

// Worlds is Valheim's world library (internal/worlds, unchanged) as the
// game contract's Worlds.
func (s *Server) Worlds() game.Worlds { return Worlds{s} }

// Worlds is the game.Worlds of the Valheim instance: a thin wrapper over
// the worlds.Library for the current folders.
type Worlds struct{ s *Server }

// Library is the worlds library for the current folders. A Library's
// folders never change; when the settings move them, a new one replaces
// it, and anything still using the old one finishes in the old folders.
func (w Worlds) Library() *worlds.Library {
	p := w.s.store.Paths()
	if lib := w.s.lib.Load(); lib != nil && lib.Worlds == p.Worlds && lib.Backups == p.Backups && lib.Staging == p.Staging {
		return lib
	}
	lib := &worlds.Library{Worlds: p.Worlds, Backups: p.Backups, Staging: p.Staging}
	w.s.lib.Store(lib)
	return lib
}

func (w Worlds) List() ([]worlds.World, []worlds.Orphan, error) { return w.Library().List() }
func (w Worlds) Get(name string) (worlds.World, error)          { return w.Library().Get(name) }
func (w Worlds) Exists(name string) bool                        { return w.Library().Exists(name) }
func (w Worlds) NameProblem(name string) string                 { return config.WorldNameProblem(name) }
func (w Worlds) Backup(name, reason string) (worlds.Backup, error) {
	return w.Library().Backup(name, reason)
}
func (w Worlds) Restore(name, backupID string) error { return w.Library().Restore(name, backupID) }
func (w Worlds) Delete(name string) error            { return w.Library().Delete(name) }
func (w Worlds) Rename(name, newName string) error   { return w.Library().Rename(name, newName) }
func (w Worlds) ExportFile(name, dir string) (string, error) {
	return w.Library().ExportFile(name, dir)
}
func (w Worlds) ExportBackup(out io.Writer, name, id string) error {
	return w.Library().ExportBackup(out, name, id)
}
func (w Worlds) Import(c []worlds.Candidate, ch []worlds.Choice, busy func(string) bool, move bool) ([]worlds.Placed, error) {
	return w.Library().Import(c, ch, busy, move)
}
func (w Worlds) CleanStaging(maxAge time.Duration) { w.Library().CleanUploads(maxAge) }

// Scan looks for worlds in an upload's folder (a worlds.Upload's Dir): in
// the files added as they are, and in each uploaded zip separately, so
// each copy of a world is its own candidate, exactly as the upload's own
// Scan does.
func (w Worlds) Scan(dir string) []worlds.Candidate {
	cands := worlds.Detect(filepath.Join(dir, "content"), 4)
	entries, _ := os.ReadDir(filepath.Join(dir, "zips"))
	var zips []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			zips = append(zips, n)
		}
	}
	sort.Ints(zips) // in the order they were uploaded
	for _, n := range zips {
		cands = append(cands, worlds.Detect(filepath.Join(dir, "zips", strconv.Itoa(n)), 4)...)
	}
	for i := range cands {
		cands[i].ID = strconv.Itoa(i + 1)
	}
	return w.Library().Check(cands)
}

// Local finds worlds saved by the Valheim game on this computer.
func (w Worlds) Local() []worlds.Candidate {
	home, _ := os.UserHomeDir()
	return w.Library().LocalCandidates(worlds.GameSaveSources(home, platform.SteamPath()))
}

// Lists are Valheim's three permission lists in the save folder. They
// apply the next time the server starts.
func (s *Server) Lists() []game.ListKind {
	return []game.ListKind{
		{ID: string(config.Admins), Label: "Admins", Placeholder: "Steam_76561198000000000", Help: "Admins can kick and ban players in the game."},
		{ID: string(config.Banned), Label: "Banned players", Placeholder: "Steam_76561198000000000", Help: "Banned players can't join."},
		{ID: string(config.Permitted), Label: "Allowed players", Placeholder: "Steam_76561198000000000", Help: "When anyone is listed, only they can join."},
	}
}

// ReadList reads one of the lists.
func (s *Server) ReadList(kind string) ([]string, error) {
	k := config.ListKind(kind)
	if k.FileName() == "" {
		return nil, fmt.Errorf("unknown list %q", kind)
	}
	return config.ReadList(s.store.Paths().Save, k)
}

// WriteList replaces one of the lists. Valheim reads them when it starts.
func (s *Server) WriteList(_ context.Context, _ *game.Record, kind string, ids []string) error {
	k := config.ListKind(kind)
	if k.FileName() == "" {
		return fmt.Errorf("unknown list %q", kind)
	}
	return config.WriteList(s.store.Paths().Save, k, ids)
}

package valheim

// The mods share code: friends import it in r2modman or Gale to get every
// mod the running world uses, at the server's versions, in one step. It's
// made when the server starts and posted in Discord with the join code.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
)

// savedCode is a share code made earlier, kept in modscodes.json so the same
// mods at the same versions keep the same code while Thunderstore has it.
type savedCode struct {
	Code string    `json:"code"`
	Made time.Time `json:"made"`
}

// After makes the running world's mods code. The online post waits for it
// (at most 45 seconds); it comes back as the record's "modsCode".
func (s *Server) After(ctx context.Context, rec game.Record) (map[string]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, codeWaitMods)
	defer cancel()
	code, err := s.modsCode(ctx, CodeMods(rec), rec.World)
	if code == "" {
		return nil, err
	}
	var r game.Record
	SetModsCode(&r, code)
	return r.Extra, err
}

// Pending says the record lacks its mods code although the world has mods:
// the app that started the server closed before making it, so the app that
// takes over makes it again.
func (s *Server) Pending(rec game.Record) bool {
	return ModsCode(rec) == "" && len(CodeMods(rec)) > 0
}

// modsCode returns the share code for the world's mods: the one made
// before for the same mods and versions while Thunderstore still has it,
// or a new one. It's empty for a world without mods.
func (s *Server) modsCode(ctx context.Context, ids []string, world string) (string, error) {
	in := &mods.Installer{Dir: s.store.Paths().Server, URL: s.store.ModsURL()}
	pkgs, err := in.ProfilePackages(ctx, ids)
	if err != nil || len(pkgs) == 0 {
		return "", err
	}
	name := mods.ProfileName(world)
	parts := []string{name}
	for _, pk := range pkgs {
		parts = append(parts, pk.Name+"-"+pk.Version)
	}
	key := strings.Join(parts, "|")

	s.codesMu.Lock()
	defer s.codesMu.Unlock()
	path := filepath.Join(s.env.Data, "modscodes.json")
	codes := map[string]savedCode{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &codes)
	}
	if c, ok := codes[key]; ok {
		// When Thunderstore can't be asked, the code most likely still works.
		if works, err := in.CodeWorks(ctx, c.Code); works || err != nil {
			return c.Code, nil
		}
	}
	profile, err := mods.Profile(name, pkgs)
	if err != nil {
		return "", err
	}
	code, err := in.ShareCode(ctx, profile)
	if err != nil {
		return "", err
	}
	codes[key] = savedCode{Code: code, Made: time.Now()}
	if len(codes) > 20 { // keep the newest
		keys := make([]string, 0, len(codes))
		for k := range codes {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return codes[b].Made.Compare(codes[a].Made) })
		for _, k := range keys[20:] {
			delete(codes, k)
		}
	}
	if b, err := json.MarshalIndent(codes, "", "  "); err == nil {
		if err := writeFileAtomic(path, b, 0o644); err != nil {
			s.log.Printf("saving modscodes.json: %v", err)
		}
	}
	return code, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PlayerMods lists the mods players need for world. For a running server
// (rec), that's the server mods it started with, at the versions it
// loaded, and players' own mods from the settings, which need no restart.
func (s *Server) PlayerMods(world string, rec *game.Record) []mods.Info {
	ids := s.store.Settings().WorldOptions[world].Mods
	versions := s.installedMods()
	var out []mods.Info
	if rec == nil || rec.Played.Mods == nil { // stopped, or started by an older version of the app
		for _, id := range ids {
			if md, ok := mods.Find(id); ok {
				out = append(out, mods.Info{Mod: md, Version: versions[md.Package]})
			}
		}
		return out
	}
	for _, md := range mods.Catalog {
		if v, ok := rec.Played.Mods[md.ID]; ok {
			out = append(out, mods.Info{Mod: md, Version: v})
		} else if md.Players == mods.PlayersOnly && slices.Contains(ids, md.ID) {
			out = append(out, mods.Info{Mod: md, Version: versions[md.Package]})
		}
	}
	return out
}

// ModsCode is the running world's mods code, while the mods players are
// told they need are still the ones it was made for.
func (s *Server) ModsCode(rec *game.Record) string {
	var ids []string
	for _, md := range s.PlayerMods(rec.World, rec) {
		ids = append(ids, md.ID)
	}
	if !slices.Equal(CodeMods(*rec), sortedIDs(ids)) {
		return ""
	}
	return ModsCode(*rec)
}

// WorldMods is every mod a world can be played with, with the version
// installed, if any.
func (s *Server) WorldMods() []mods.Info {
	installed := (&mods.Installer{Dir: s.store.Paths().Server}).Installed()
	out := make([]mods.Info, 0, len(mods.Catalog))
	for _, md := range mods.Catalog {
		out = append(out, mods.Info{Mod: md, Version: installed[md.Package]})
	}
	return out
}

// SetWorldMods changes which mods a world is played with, keeping its
// difficulty settings.
func (s *Server) SetWorldMods(world string, ids []string) error {
	return s.store.Update(func(next *config.Settings) error {
		if next.WorldOptions == nil {
			next.WorldOptions = map[string]config.WorldOptions{}
		}
		o := next.WorldOptions[world].Clone()
		o.Mods = ids
		if msg := o.Problem(); msg != "" {
			return errors.New(msg)
		}
		if o.IsZero() {
			delete(next.WorldOptions, world)
		} else {
			next.WorldOptions[world] = o.Clone()
		}
		return nil
	})
}

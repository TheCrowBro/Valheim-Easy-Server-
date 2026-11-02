package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/worlds"
)

// A new world doesn't get the difficulty and mods of a deleted world that
// had the same name.
func TestNewWorldDoesNotInheritDeletedWorldsOptions(t *testing.T) {
	env := setup(t)
	m := env.m
	// "Old" was played hardcore with Riverheim, then deleted. Its options
	// are kept, so that restoring it would bring them back.
	if err := m.SetWorldOptions("Old", config.WorldOptions{Preset: "hardcore"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorldMods("Old", []string{"riverheim"}); err != nil {
		t.Fatal(err)
	}
	// A new "Old" is made with the default difficulty and no mods.
	if err := m.CreateWorld("Old", config.WorldOptions{}, false); err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if o, ok := s.WorldOptions["Old"]; ok {
		t.Fatalf("the new world got the old one's options: %+v", o)
	}
	if mods := s.WorldMods(); len(mods) != 0 {
		t.Fatalf("the new world got mods %v", mods)
	}
	args, err := s.ServerArgs("x", "y")
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(args, " "); strings.Contains(line, "-preset") {
		t.Fatalf("args = %v", args)
	}
}

// Renaming a world onto a deleted world's name doesn't give it the deleted
// world's difficulty and mods either.
func TestRenamedWorldDoesNotInheritDeletedWorldsOptions(t *testing.T) {
	env := setup(t)
	m := env.m
	// "Old" was played hardcore with Riverheim, then deleted; its options
	// are kept.
	if err := m.SetWorldOptions("Old", config.WorldOptions{Preset: "hardcore"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorldMods("Old", []string{"riverheim"}); err != nil {
		t.Fatal(err)
	}
	// "Other" is a classic world with no options of its own.
	dir := m.library().Worlds
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := worlds.BuildMeta(worlds.Meta{Version: 35, Name: "Other", SeedName: "S", Seed: 1, UID: 2}, true)
	if err := os.WriteFile(filepath.Join(dir, "Other.fwl"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Other.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SelectWorld("Other", false); err != nil {
		t.Fatal(err)
	}
	if err := m.RenameWorld("Other", "Old"); err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if s.World != "Old" {
		t.Fatalf("selected = %q", s.World)
	}
	if o, ok := s.WorldOptions["Old"]; ok {
		t.Fatalf("the renamed world got the deleted one's options: %+v", o)
	}
	if mods := s.WorldMods(); len(mods) != 0 {
		t.Fatalf("the renamed world got mods %v", mods)
	}
	args, err := s.ServerArgs("x", "y")
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(args, " "); strings.Contains(line, "-preset") {
		t.Fatalf("args = %v", args)
	}
}

// A world that's renamed keeps its own difficulty and mods.
func TestRenamedWorldKeepsItsOptions(t *testing.T) {
	env := setup(t)
	m := env.m
	dir := m.library().Worlds
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := worlds.BuildMeta(worlds.Meta{Version: 35, Name: "Other", SeedName: "S", Seed: 1, UID: 2}, true)
	if err := os.WriteFile(filepath.Join(dir, "Other.fwl"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Other.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorldOptions("Other", config.WorldOptions{Preset: "casual"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RenameWorld("Other", "New"); err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if _, ok := s.WorldOptions["Other"]; ok {
		t.Fatalf("options left under the old name: %+v", s.WorldOptions)
	}
	if o := s.WorldOptions["New"]; o.Preset != "casual" {
		t.Fatalf("the renamed world lost its options: %+v", o)
	}
}

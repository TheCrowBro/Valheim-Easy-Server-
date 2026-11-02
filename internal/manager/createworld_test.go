package manager

import (
	"strings"
	"testing"

	"valheim-easy-server/internal/config"
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

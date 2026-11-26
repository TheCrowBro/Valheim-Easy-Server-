package install

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

func TestBuildFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "server")
	if _, ok := ReadBuild(dir); ok {
		t.Error("ReadBuild found a build in an empty folder")
	}
	want := game.Build{Version: "1.4.6.1", Date: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	if err := WriteBuild(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadBuild(dir)
	if !ok || got.Version != want.Version || !got.Date.Equal(want.Date) {
		t.Errorf("ReadBuild = %+v, %v", got, ok)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".egs-build.json"))
	if string(b) != "{\n  \"version\": \"1.4.6.1\",\n  \"date\": \"2026-11-26T12:00:00Z\"\n}\n" {
		t.Errorf(".egs-build.json = %s", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("WriteBuild left %v", entries)
	}

	for _, bad := range []string{"not json", "{}", `{"date":"2026-11-26T12:00:00Z"}`} {
		os.WriteFile(filepath.Join(dir, ".egs-build.json"), []byte(bad), 0o644)
		if b, ok := ReadBuild(dir); ok {
			t.Errorf("ReadBuild of %q = %+v", bad, b)
		}
	}
}

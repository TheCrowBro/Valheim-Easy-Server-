package platform

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKnownFolder(t *testing.T) {
	names := []string{"appdata", "localappdata", "documents", "savedgames", "home", "appsupport"}
	want := map[string][]string{ // the names each system has
		"windows": {"appdata", "localappdata", "documents", "savedgames", "home"},
		"darwin":  {"documents", "home", "appsupport"},
		"linux":   {"documents", "home"},
	}[runtime.GOOS]
	for _, name := range names {
		got := KnownFolder(name)
		t.Logf("%s = %q", name, got)
		has := false
		for _, w := range want {
			has = has || w == name
		}
		if has && (got == "" || !filepath.IsAbs(got)) {
			t.Errorf("KnownFolder(%q) = %q, want a full path", name, got)
		}
		if !has && got != "" {
			t.Errorf("KnownFolder(%q) = %q, want nothing on %s", name, got, runtime.GOOS)
		}
	}
	home := KnownFolder("home")
	for _, name := range want {
		if got := KnownFolder(name); name != "home" && !strings.HasPrefix(strings.ToLower(got), strings.ToLower(home)) && runtime.GOOS != "windows" {
			t.Errorf("KnownFolder(%q) = %q isn't in the home folder %q", name, got, home)
		}
	}
	if runtime.GOOS == "darwin" && !strings.HasSuffix(KnownFolder("appsupport"), "/Library/Application Support") {
		t.Errorf("appsupport = %q", KnownFolder("appsupport"))
	}
	if got := KnownFolder("nonsense"); got != "" {
		t.Errorf("KnownFolder(nonsense) = %q", got)
	}
}

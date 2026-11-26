package game

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoTunnel: the owner decided there is no tunnel of any kind (no
// playit.gg, no ngrok, nothing like them): no package, route, setting, text
// or release item may offer one (invariant I10). Friends outside the home
// reach a game through router ports instead. So the words may not appear
// anywhere in the tree, in any case, except in the design notes under
// docs/dev and in this file, which has to name them.
func TestNoTunnel(t *testing.T) {
	root := moduleRoot(t)
	words := regexp.MustCompile(`(?i)playit|ngrok|tunnel`)
	self := filepath.Join("internal", "game", "notunnel_test.go")
	skipDirs := map[string]bool{
		filepath.Join("docs", "dev"): true,
		// Local build output and data, never part of the tree (.gitignore).
		"dist": true, "data": true, "dev-data": true,
	}
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			// Hidden folders are git's and editors' (and worktrees inside
			// .claude), except .github, which holds the release workflow.
			if rel != "." && (skipDirs[rel] || (strings.HasPrefix(d.Name(), ".") && d.Name() != ".github")) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == self || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Programs, images and archives aren't text anyone reads.
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			return nil
		}
		checked++
		for i, line := range strings.Split(string(b), "\n") {
			if m := words.FindString(line); m != "" {
				t.Errorf("%s:%d: %q: the app has no tunnel of any kind (decided); friends outside use router ports", filepath.ToSlash(rel), i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("checked only %d files; the walk missed the tree", checked)
	}
}

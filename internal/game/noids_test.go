package game

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// decidedIDs are the ids of the eleven games decided with the owner. They
// are listed here as well as taken from the registry, because this package
// can't import internal/games/all (every adapter imports this package), so
// in its own tests nothing is registered; listing them also catches an id
// written into the manager before its game is registered.
var decidedIDs = []string{
	"valheim", "minecraft", "terraria", "necesse", "vintagestory", "zomboid",
	"enshrouded", "corekeeper", "palworld", "satisfactory", "windrose",
}

func gameIDs() []string {
	ids := slices.Clone(decidedIDs)
	for _, g := range All() {
		if id := g.Info().ID; !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// goStringLiterals returns each string literal in a Go file with its line.
func goStringLiterals(t *testing.T, path string) map[string][]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[s] = append(out[s], fset.Position(lit.Pos()).Line)
			}
		}
		return true
	})
	return out
}

// TestNoGameIDs: the manager, the web server and the panel's shell never
// name a game (spec 2.1). They find the legacy game with game.Legacy() and
// ask info.Legacy where Valheim must behave as today, so a new game needs
// no change there and Valheim's special cases can't leak onto other games.
// Test files and the panel's per-game modules (assets/games/) may name games.
func TestNoGameIDs(t *testing.T) {
	root := moduleRoot(t)
	ids := gameIDs()

	var goFiles []string
	for _, dir := range []string{"internal/manager", "internal/web"} {
		m, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m {
			if !strings.HasSuffix(f, "_test.go") {
				goFiles = append(goFiles, f)
			}
		}
	}
	if len(goFiles) == 0 {
		t.Fatal("found no manager or web files to check")
	}
	for _, f := range goFiles {
		lits := goStringLiterals(t, f)
		for _, id := range ids {
			for _, line := range lits[id] {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d: the string %q is a game id; use the registry (game.Legacy(), Info.Legacy) instead", filepath.ToSlash(rel), line, id)
			}
		}
	}

	// The panel's shell: a JavaScript string that is exactly an id, in any
	// of the three quotes.
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = regexp.QuoteMeta(id)
	}
	alt := strings.Join(quoted, "|")
	re := regexp.MustCompile(`"(?:` + alt + `)"|'(?:` + alt + `)'|` + "`(?:" + alt + ")`")
	jsFiles, err := filepath.Glob(filepath.Join(root, "internal", "web", "ui", "assets", "*.js"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jsFiles) == 0 {
		t.Fatal("found no panel scripts to check")
	}
	for _, f := range jsFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range re.FindAllString(line, -1) {
				rel, _ := filepath.Rel(root, f)
				t.Errorf("%s:%d: the string %s is a game id; the panel's shell gets games from the status, and game-specific code goes in assets/games/", filepath.ToSlash(rel), i+1, m)
			}
		}
	}
}

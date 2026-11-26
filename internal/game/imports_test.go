package game

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// moduleRoot finds the folder holding go.mod, above this package's folder
// (where go test runs it).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's folder")
		}
		dir = parent
	}
}

// modulePath reads the module's import path from go.mod.
func modulePath(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// importProblems applies spec 2.1's dependency rules to each package's
// direct imports (non-test files only). Packages are named by import path;
// mod is the module's path. It returns one line per broken rule.
//
// The rules keep the game contract free of everything but data types, keep
// each game's adapter apart from the manager, the web server and the other
// games, and keep game code out of the manager and the web server, so the
// manager can only ever reach a game through the contract.
func importProblems(mod string, pkgs map[string][]string) []string {
	rel := func(p string) (string, bool) {
		if p == mod {
			return "", true
		}
		return strings.CutPrefix(p, mod+"/")
	}
	firstElem := func(p string) string { e, _, _ := strings.Cut(p, "/"); return e }
	std := func(p string) bool {
		_, own := rel(p)
		return !own && !strings.Contains(firstElem(p), ".")
	}
	under := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

	gameMayUse := []string{"internal/worlds", "internal/mods"}
	adapterMayUse := []string{"internal/game", "internal/install", "internal/rcon", "internal/a2s", "internal/worldstore", "internal/platform"}
	legacyMayAlsoUse := []string{"internal/config", "internal/logparse", "internal/mods", "internal/worlds", "internal/steamcmd"}
	const legacyID = "valheim" // the one adapter that wraps today's Valheim packages (spec 2.1)

	var out []string
	for _, pkg := range slices.Sorted(maps.Keys(pkgs)) {
		p, own := rel(pkg)
		if !own {
			continue
		}
		bad := func(imp, why string) { out = append(out, fmt.Sprintf("%s imports %s: %s", p, imp, why)) }
		for _, imp := range pkgs[pkg] {
			i, ownImp := rel(imp)
			switch {
			case p == "internal/game":
				if !std(imp) && !(ownImp && slices.Contains(gameMayUse, i)) {
					bad(imp, "the game contract may import only the standard library, internal/worlds and internal/mods")
				}

			case p == "internal/games/all":
				if ownImp && !(i == "internal/game" || (under(i, "internal/games") && !under(i, "internal/games/all"))) {
					bad(imp, "internal/games/all only imports the game adapters to register them")
				}

			case under(p, "internal/games"):
				id, _, _ := strings.Cut(strings.TrimPrefix(p, "internal/games/"), "/")
				switch {
				case !ownImp:
				case under(i, "internal/games/"+id):
				case slices.Contains(adapterMayUse, i):
				case id == legacyID && slices.Contains(legacyMayAlsoUse, i):
				case under(i, "internal/games"):
					bad(imp, "a game's adapter never imports another game")
				case under(i, "internal/manager"), under(i, "internal/web"):
					bad(imp, "a game's adapter never imports the manager or the web server")
				default:
					bad(imp, "a game's adapter may import only game, install, rcon, a2s, worldstore and platform (and, for Valheim, config, logparse, mods, worlds and steamcmd)")
				}

			case p == "internal/manager", p == "internal/web":
				if ownImp && under(i, "internal/games") {
					bad(imp, "the manager and the web server reach games only through internal/game and its registry (main.go imports internal/games/all)")
				}
			}
		}
	}
	return out
}

// TestImportRules checks spec 2.1's dependency rules on the real tree, for
// each system the app is built for (some files only build on one of them).
func TestImportRules(t *testing.T) {
	root := moduleRoot(t)
	mod := modulePath(t, root)
	for _, goos := range []string{"linux", "windows", "darwin"} {
		cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GOOS=%s go list: %v\n%s", goos, err, stderr.Bytes())
		}
		pkgs := map[string][]string{}
		for line := range strings.Lines(string(out)) {
			f := strings.Fields(line)
			if len(f) > 0 {
				pkgs[f[0]] = f[1:]
			}
		}
		if _, ok := pkgs[mod+"/internal/game"]; !ok {
			t.Fatalf("GOOS=%s: go list didn't list internal/game; the rules weren't checked", goos)
		}
		for _, p := range importProblems(mod, pkgs) {
			t.Errorf("GOOS=%s: %s", goos, p)
		}
	}
}

// TestImportRulesCatch makes sure the rules above really catch what they
// are for, so a slip in importProblems can't let everything through.
func TestImportRulesCatch(t *testing.T) {
	const m = "example"
	pkgs := map[string][]string{
		m + "/internal/game":            {"context", m + "/internal/worlds", m + "/internal/mods", m + "/internal/config", "golang.org/x/net/websocket"},
		m + "/internal/games/minecraft": {"os", "golang.org/x/sys/unix", m + "/internal/game", m + "/internal/rcon", m + "/internal/games/minecraft/resolve", m + "/internal/config", m + "/internal/games/terraria", m + "/internal/manager", m + "/internal/game/gametest"},
		m + "/internal/games/valheim":   {m + "/internal/game", m + "/internal/config", m + "/internal/steamcmd", m + "/internal/logparse", m + "/internal/web"},
		m + "/internal/games/all":       {m + "/internal/games/valheim", m + "/internal/games/minecraft", m + "/internal/manager"},
		m + "/internal/manager":         {m + "/internal/game", m + "/internal/config", m + "/internal/games/valheim", m + "/internal/games/all"},
		m + "/internal/web":             {m + "/internal/manager", m + "/internal/game", m + "/internal/games/minecraft"},
		m + "/internal/servers":         {m + "/internal/game"},
	}
	got := importProblems(m, pkgs)
	want := []string{
		"internal/game imports example/internal/config",
		"internal/game imports golang.org/x/net/websocket",
		"internal/games/all imports example/internal/manager",
		"internal/games/minecraft imports example/internal/config",
		"internal/games/minecraft imports example/internal/games/terraria",
		"internal/games/minecraft imports example/internal/manager",
		"internal/games/minecraft imports example/internal/game/gametest",
		"internal/games/valheim imports example/internal/web",
		"internal/manager imports example/internal/games/valheim",
		"internal/manager imports example/internal/games/all",
		"internal/web imports example/internal/games/minecraft",
	}
	if len(got) != len(want) {
		t.Errorf("got %d problems, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, w+":") }) {
			t.Errorf("not caught: %s", w)
		}
	}
}

package gametest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var fakeBuild struct {
	once sync.Once
	path string
	err  error
}

// BuildFakeserver builds cmd/fakeserver once per test binary and returns
// the program's path.
func BuildFakeserver(t testing.TB) string {
	t.Helper()
	fakeBuild.once.Do(func() { fakeBuild.path, fakeBuild.err = buildFakeserver() })
	if fakeBuild.err != nil {
		t.Fatalf("gametest: building cmd/fakeserver: %v", fakeBuild.err)
	}
	return fakeBuild.path
}

func buildFakeserver() (string, error) {
	mod := strings.TrimSuffix(reflect.TypeFor[Options]().PkgPath(), "/internal/game/gametest")
	pkg := mod + "/cmd/fakeserver"
	if err := readSources(pkg); err != nil {
		return "", err
	}
	dir, err := buildDir()
	if err != nil {
		return "", err
	}
	name := "fakeserver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v\n%s", err, b)
	}
	return out, nil
}

// buildDir is where the stand-in is built: next to the test binary when
// go test made that in its own temporary folder, which go test removes
// when the run ends; otherwise a temporary folder of its own.
func buildDir() (string, error) {
	if exe, err := os.Executable(); err == nil && strings.Contains(filepath.ToSlash(exe), "/go-build") {
		if d, err := os.MkdirTemp(filepath.Dir(exe), "fakeserver-"); err == nil {
			return d, nil
		}
	}
	return os.MkdirTemp("", "egs-fakeserver-")
}

// readSources reads every source file the stand-in is built from. go test
// remembers the files a test opened and reruns a cached test when one of
// them changes, so a changed stand-in is never tested with an old result.
func readSources(pkg string) error {
	out, err := exec.Command("go", "list", "-deps", "-f", `{{if not .Standard}}{{.Dir}}{{range .GoFiles}}|{{.}}{{end}}{{end}}`, pkg).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%v: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return fmt.Errorf("go list %s (run the tests from inside the module): %v", pkg, err)
	}
	for line := range strings.Lines(string(out)) {
		parts := strings.Split(strings.TrimSpace(line), "|")
		for _, f := range parts[1:] {
			if _, err := os.ReadFile(filepath.Join(parts[0], f)); err != nil {
				return err
			}
		}
	}
	return nil
}

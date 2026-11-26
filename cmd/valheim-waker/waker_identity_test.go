package main

// The waker's flags and where it keeps its files on the JetKVM
// (docs/dev/multigame/spec.md 1.2). The start script that a copy of the app
// put on the JetKVM runs whatever waker is there with these flags, and the
// app checks a new waker with -check before it swaps it in, so they never
// change. Each value is compared with its literal: renaming one fails here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// runWakerEnv makes this test program run the waker's main instead of the
// tests (see init): its flags are made inside main.
const runWakerEnv = "VES_TEST_RUN_WAKER"

func init() {
	if os.Getenv(runWakerEnv) == "1" {
		main()
		os.Exit(0)
	}
}

func runWaker(t *testing.T, want int, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), runWakerEnv+"=1")
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the waker with %q: %v", args, err)
	}
	if code != want {
		t.Fatalf("the waker with %q exited with %d, want %d:\n%s", args, code, want, out)
	}
	return string(out)
}

func TestIdentity(t *testing.T) {
	usage := runWaker(t, 0, "-h")
	flags := map[string]string{} // name: type and what -h says about it
	last := ""
	for _, line := range strings.Split(usage, "\n") {
		if m := regexp.MustCompile(`^  -(\S+)(?: (\S+))?$`).FindStringSubmatch(line); m != nil {
			kind := m[2]
			if kind == "" {
				kind = "bool"
			}
			flags[m[1]], last = kind, m[1]
		} else if last != "" {
			flags[last] += line
		}
	}
	for name, want := range map[string][]string{
		"config":  {"string", `(default "/userdata/valheim-waker/config.json")`},
		"status":  {"string", `(default "/tmp/valheim-waker.json")`},
		"check":   {"bool"},
		"version": {"bool"},
	} {
		got, ok := flags[name]
		if !ok {
			t.Errorf("the waker has no -%s flag:\n%s", name, usage)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("-%s: %q doesn't say %q", name, got, w)
			}
		}
	}
	// The app compares the waker's version with its own.
	if out := runWaker(t, 0, "-version"); strings.TrimSpace(out) != "dev" {
		t.Errorf("-version printed %q, want dev", out)
	}
}

package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// Fake is the installer a game's adapter returns in fake mode (Env.Fake
// set, by tests or EGS_DEV_FAKE_SERVERS): instead of the real server it
// puts the stand-in (cmd/fakeserver) where the real server's program would
// be, so the rest of the app runs the game exactly as it would run the real
// one (spec 7.1).
//
// It copies f.Program to program (Server.Program(), which in fake mode is
// laid out the same on every system, ending in ".exe" only on Windows) and
// writes beside it:
//   - fakeserver.json, the stand-in's settings for this copy: f.Script and
//     f.Speed. The stand-in finds its game from the command line the
//     adapter's Plan gives it, so this file doesn't name the game.
//   - .egs-build.json, the build ReadBuild reports ("fake"), so Build() says
//     the server is installed.
//
// files are the other files the adapter's Plan or Build expect, as
// absolute paths (or relative to program's folder). Each is made empty,
// unless it exists already, with two exceptions that get content:
// "steamapps/appmanifest_<id>.acf" gets a Steam manifest that SteamBuild
// reads as build 1, and ".egs-build.json" gets the fake build record.
// .egs-build.json is also written in the folder of every file listed, so a
// Build() that reads ReadBuild(Dirs.Server) finds it whether the program
// is in the server folder or, like a private Java, somewhere else.
//
// f.Delay makes the install take that long, reporting progress, as a
// download would; cancelling ctx stops it.
func Fake(f *game.Fake, program string, files []string) game.Installer {
	return &fakeInstaller{fake: f, program: program, files: files}
}

type fakeInstaller struct {
	fake    *game.Fake
	program string
	files   []string
}

// fakeStepText is the progress text of a fake install.
const fakeStepText = "Installing the stand-in server"

// fakeAppManifest matches the Steam manifests Fake fills in.
var fakeAppManifest = regexp.MustCompile(`^appmanifest_(\d+)\.acf$`)

func (i *fakeInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if progress == nil {
		progress = func(game.Progress) {}
	}
	if i.fake == nil || i.fake.Program == "" {
		return errors.New("there is no stand-in server to install (fake mode needs the path of the built cmd/fakeserver)")
	}
	if !filepath.IsAbs(i.program) {
		return fmt.Errorf("the stand-in server needs a full path to install to, not %q", i.program)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(i.program), ".exe") {
		// Windows only starts a program whose name ends in .exe.
		return fmt.Errorf("on Windows the stand-in server must be installed as a .exe, not %s", filepath.Base(i.program))
	}

	if err := fakeWait(ctx, i.fake.Delay, progress); err != nil {
		return err
	}
	dir := filepath.Dir(i.program)
	if err := fakeCopyProgram(i.fake.Program, i.program); err != nil {
		return fmt.Errorf("couldn't install the stand-in server: %w", err)
	}
	conf := struct {
		Script string  `json:"script,omitempty"`
		Speed  float64 `json:"speed,omitempty"`
	}{i.fake.Script, i.fake.Speed}
	if err := fakeWriteJSON(filepath.Join(dir, "fakeserver.json"), conf); err != nil {
		return err
	}

	build := game.Build{Version: "fake", Date: time.Now().UTC().Truncate(time.Second)}
	buildDirs := []string{dir}
	for _, name := range i.files {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		base := filepath.Base(path)
		var err error
		switch m := fakeAppManifest.FindStringSubmatch(base); {
		case base == ".egs-build.json":
			// written with the others below
		case m != nil:
			err = os.WriteFile(path, []byte(fakeManifest(m[1], build.Date)), 0o644)
		default:
			err = fakePlaceholder(path)
		}
		if err != nil {
			return err
		}
		buildDirs = append(buildDirs, filepath.Dir(path))
	}
	done := map[string]bool{}
	for _, d := range buildDirs {
		if done[d] {
			continue
		}
		done[d] = true
		if err := fakeWriteJSON(filepath.Join(d, ".egs-build.json"), build); err != nil {
			return err
		}
	}
	progress(game.Progress{Step: fakeStepText, Percent: 100})
	return nil
}

// fakeWait takes delay, as the download would, reporting progress about
// every tenth of it.
func fakeWait(ctx context.Context, delay time.Duration, progress func(game.Progress)) error {
	start := time.Now()
	tick := time.NewTicker(max(delay/10, 10*time.Millisecond))
	defer tick.Stop()
	for {
		done := time.Since(start)
		if done >= delay {
			return nil
		}
		progress(game.Progress{Step: fakeStepText, Percent: 100 * float64(done) / float64(delay)})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// fakeCopyProgram copies the stand-in into place through a temporary file
// in the same folder, so a half-copied program is never left where the app
// would run it.
func fakeCopyProgram(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(to), ".fakeserver-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), to)
}

// fakePlaceholder makes an empty file, leaving one that exists alone (a
// repair mustn't wipe a file the stand-in may have written).
func fakePlaceholder(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// fakeManifest is a Steam app manifest with build 1, enough for SteamBuild.
func fakeManifest(appID string, updated time.Time) string {
	return "\"AppState\"\n{\n" +
		"\t\"appid\"\t\t\"" + appID + "\"\n" +
		"\t\"name\"\t\t\"stand-in server\"\n" +
		"\t\"StateFlags\"\t\t\"4\"\n" +
		"\t\"buildid\"\t\t\"1\"\n" +
		"\t\"LastUpdated\"\t\t\"" + strconv.FormatInt(updated.Unix(), 10) + "\"\n" +
		"}\n"
}

// fakeWriteJSON writes v as indented JSON, whole or not at all.
func fakeWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

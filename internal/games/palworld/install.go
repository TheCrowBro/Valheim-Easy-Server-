package palworld

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// steamApp is the dedicated server on Steam. Installed it takes 5.75 GB on
// Windows (FACTS.md); the free-space check asks for a little more.
var steamApp = install.SteamApp{AppID: appID, Name: "Palworld", SizeGB: 6}

// vcRuntime is the Microsoft Visual C++ runtime (x64) the server needs on
// Windows. Steam installs it with the server; SteamCMD doesn't run Steam's
// installers, so the app installs it itself (FACTS.md). The link is
// Microsoft's permanent one for the newest runtime, which changes with
// every release, so there is no fixed checksum to compare; it is only ever
// fetched over HTTPS from Microsoft.
var vcRuntime = install.Prereq{
	Name:      "the Microsoft Visual C++ runtime",
	Installed: vcRuntimeOK,
	URL:       "https://aka.ms/vc14/vc_redist.x64.exe",
	Args:      []string{"/install", "/quiet", "/norestart"},
}

// needsVCRuntime: only the Windows server needs the runtime. Tests set it
// to check the install's order on any system.
var needsVCRuntime = runtime.GOOS == "windows"

// Installer downloads the server with the shared SteamCMD (after the
// Visual C++ runtime on Windows), or puts the stand-in in its place in fake
// mode.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		files := []string{
			s.defaultSettingsPath(),
			filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf"),
		}
		return &fakeInstaller{inner: install.Fake(s.env.Fake, s.Program(), files), defaults: s.defaultSettingsPath()}
	}
	return &realInstaller{prereq: install.EnsurePrereq, steam: s.steam()}
}

// steam is the SteamCMD installer alone: a start's update uses it, because
// a start must never wait for an administrator prompt nobody may be there
// to answer (it may come from Discord or a wake).
func (s *server) steam() game.Installer {
	return install.SteamCMD(steamApp, s.env.Dirs, func(line string) { s.log.Print(line) })
}

type realInstaller struct {
	prereq func(context.Context, install.Prereq, func(game.Progress)) error
	steam  game.Installer
}

// Install puts the runtime in place first, so a "No" to Windows' prompt
// stops the install before the 4.7 GB download rather than after it.
//
// A repair always runs the runtime's installer, even when Windows lists the
// runtime: Repair is what ExitHint suggests when the server stops before it
// writes anything, and an old or damaged runtime is the likeliest reason.
// The installer brings an older runtime up to date and leaves a current one
// as it is.
func (i *realInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if progress == nil {
		progress = func(game.Progress) {}
	}
	if needsVCRuntime {
		p := vcRuntime
		if repair {
			p.Installed = nil
		}
		if err := i.prereq(ctx, p, progress); err != nil {
			return err
		}
		if repair && !vcRuntime.Installed() {
			return errors.New("The Visual C++ runtime installer finished, but Windows doesn't list a new enough runtime. Restart the computer and try Repair again.")
		}
	}
	return i.steam.Install(ctx, repair, progress)
}

// fakeInstaller is install.Fake, plus the shipped default settings file:
// the stand-in reads what the plan copies from it, as the real server does.
type fakeInstaller struct {
	inner    game.Installer
	defaults string
}

func (f *fakeInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if err := f.inner.Install(ctx, repair, progress); err != nil {
		return err
	}
	if st, err := os.Stat(f.defaults); err == nil && st.Size() > 0 {
		return nil
	}
	return os.WriteFile(f.defaults, defaultSettings, 0o644)
}

// Build is what's installed: Steam's build, as long as the server program
// is still there. Palworld's own version ("v1.0.5.102999") isn't in any
// file Steam writes; the Monitor reads it from the REST API and the log.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.SteamBuild(s.env.Dirs.Server, appID)
	if !ok {
		return game.Build{}, false
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Build{}, false
	}
	return b, true
}

// installed says why the server can't start yet, or nil.
func (s *server) installed() error {
	if _, err := os.Stat(s.Program()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("The Palworld server isn't installed. Click Install (or Repair) first.")
		}
		return err
	}
	return nil
}

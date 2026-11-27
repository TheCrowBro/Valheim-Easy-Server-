package windrose

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// steamApp is the dedicated server on Steam. Installed it takes 3.1 GB on
// Windows (FACTS.md); the free-space check asks for a little more.
var steamApp = install.SteamApp{AppID: appID, Name: "Windrose", SizeGB: 3.5}

// vcRuntime is the Microsoft Visual C++ runtime (x64) the server needs on
// Windows: its program links MSVCP140.dll and VCRUNTIME140.dll, and the
// launcher Steam would start (WindroseServer.exe) only checks for this
// runtime and offers to install it. The app starts the server's program
// itself, so it installs the runtime itself (FACTS.md). The link is
// Microsoft's permanent one for the newest runtime, which changes with
// every release, so there is no fixed checksum to compare; it is only
// ever fetched over HTTPS from Microsoft.
//
// Name is Microsoft's own name for it, with no "the": install.EnsurePrereq
// starts sentences with it and puts it after its own "The".
var vcRuntime = install.Prereq{
	Name:      "Microsoft Visual C++ Redistributable",
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
//
// The server writes its settings file and its first world the first time
// it starts (the publisher: "Start and stop the server once to generate
// them"). The app does that run at the first start rather than here
// (firstrun.go): the firewall rule names the installed program, so it can
// only be made once the install is done, and the first start is the first
// moment it can be in place before the server listens; a settings file
// lost later is also made again the same way.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), []string{
			filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf"),
		})
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
// stops the install before the 3 GB download rather than after it.
//
// A repair always runs the runtime's installer, even when Windows lists the
// runtime: Repair is what ExitHint suggests when the server stops before it
// writes anything, and a missing or damaged runtime is the likeliest reason
// (Windows then shows its own "MSVCP140.dll was not found" box, which the
// hidden server can't). The installer brings an older runtime up to date
// and leaves a current one as it is.
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

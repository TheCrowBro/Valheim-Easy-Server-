package satisfactory

import (
	"errors"
	"os"
	"path/filepath"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// steamApp is the dedicated server on Steam. Installed it takes 15.5 GB on
// Windows (FACTS.md); the free-space check asks for a little more.
var steamApp = install.SteamApp{AppID: appID, Name: "Satisfactory", SizeGB: 16}

// Installer downloads the server with the shared SteamCMD, or puts the
// stand-in in its place in fake mode.
//
// The server needs no separate prerequisite: SteamCMD brings Steam's own
// library with it (depot 1004, from Steamworks' redistributables), and the
// rest is in the server's files (FACTS.md).
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		files := []string{filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf")}
		return install.Fake(s.env.Fake, s.Program(), files)
	}
	return s.steam()
}

// steam is the SteamCMD installer. A start's update uses it with
// install.SkipIfBusy, so a start never waits for another game's download.
func (s *server) steam() game.Installer {
	return install.SteamCMD(steamApp, s.env.Dirs, func(line string) { s.log.Print(line) })
}

// Build is what's installed: Steam's build, as long as the server program
// is still there. Satisfactory's own version ("1.2.0", build 520140) isn't
// in any file Steam writes; the Monitor reads it from the log.
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
			return errors.New("The Satisfactory server isn't installed. Click Install (or Repair) first.")
		}
		return err
	}
	return nil
}

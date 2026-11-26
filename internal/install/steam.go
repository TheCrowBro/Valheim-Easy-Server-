package install

import (
	"context"
	"errors"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/steamcmd"
)

// SteamCMD installs a Steam app with the shared SteamCMD in dirs.SteamCMD. Only one
// SteamCMD runs at a time in the whole app (it updates itself and keeps one package cache):
// an install or repair waits, showing "Waiting for the <game> download to finish…".
// An update-on-start never waits (see below).
//
// The installer is internal/steamcmd's, the one Valheim has always used, given
// the app's id, branch, name and size; it shares Valheim's lock. To update a
// server at the start of a game, call Install with SkipIfBusy(ctx): while
// another game's download holds SteamCMD it returns a *BusyError at once, and
// the start goes on with the build that is installed.
func SteamCMD(app SteamApp, dirs game.Dirs, out func(string)) game.Installer {
	return &steamInstaller{s: &steamcmd.SteamCMD{
		Dir:        dirs.SteamCMD,
		InstallDir: dirs.Server,
		Output:     out,
		AppID:      app.AppID,
		Branch:     app.Branch,
		Name:       app.Name,
		SizeGB:     app.SizeGB,
	}}
}

// SteamApp is the Steam app a game's dedicated server is.
type SteamApp struct {
	AppID  string  // "1169370"
	Branch string  // "" = public
	Name   string  // "Necesse" (progress and error texts)
	SizeGB float64 // for "not enough free disk space for the Palworld server (it needs about 6 GB)"
}

type steamInstaller struct {
	s *steamcmd.SteamCMD
}

func (i *steamInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if i.s.Dir == "" || i.s.InstallDir == "" {
		return errors.New("the game has no SteamCMD or server folder set")
	}
	if i.s.AppID == "" {
		// An empty app id means Valheim's to internal/steamcmd; another
		// game that forgot its own must not install Valheim by mistake.
		return errors.New("the game has no Steam app id set")
	}
	return i.s.Install(ctx, repair, func(p steamcmd.Progress) {
		if progress != nil {
			progress(game.Progress{Step: p.Step, Percent: p.Percent})
		}
	})
}

// SkipIfBusy marks ctx for a start's update: an Install under it never
// waits for another game's SteamCMD download, it returns a *BusyError
// straight away (spec 2.3). The manager's warning then reads "Couldn't
// check for server updates (Satisfactory is downloading). Starting the
// version that's installed." Installs and repairs don't use it: they wait.
func SkipIfBusy(ctx context.Context) context.Context {
	return steamcmd.SkipIfBusy(ctx)
}

// ErrBusy is what errors.Is finds in an Install's error when it skipped
// because another game's download holds SteamCMD.
var ErrBusy = steamcmd.ErrBusy

// BusyError names the game whose download holds SteamCMD; its text is
// "<game> is downloading".
type BusyError = steamcmd.BusyError

// SteamBuild reads what SteamCMD installed: steamapps/appmanifest_<id>.acf.
func SteamBuild(serverDir, appID string) (game.Build, bool) {
	id, updated, ok := steamcmd.InstalledBuildFor(serverDir, appID)
	if !ok {
		return game.Build{}, false
	}
	return game.Build{Build: id, Date: updated}, true
}

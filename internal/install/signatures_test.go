package install

import (
	"context"
	"testing"

	"valheim-easy-server/internal/game"
)

// These pin the phase 0 contract (spec 2.3): the game streams call these
// functions while this package is being written, so changing one of them
// needs the lead, and must change this file too.
var (
	_ func(SteamApp, game.Dirs, func(string)) game.Installer                     = SteamCMD
	_ func(string, string) (game.Build, bool)                                    = SteamBuild
	_ func(context.Context, string, Hash, string, func(game.Progress)) error     = Download
	_ func(string, string, func(string) bool) error                              = Unzip
	_ func(string, string) error                                                 = Untar
	_ func(context.Context, int, string, func(game.Progress)) (string, error)    = Temurin
	_ func(context.Context, string, string, func(game.Progress)) (string, error) = DotNet
	_ func(context.Context, Prereq, func(game.Progress)) error                   = EnsurePrereq
	_ func(string, game.Build) error                                             = WriteBuild
	_ func(string) (game.Build, bool)                                            = ReadBuild

	// Added by stream I: a start's update that never waits (spec 2.3), and
	// the runtime a game installed, found without going online.
	_ func(context.Context) context.Context = SkipIfBusy
	_ func(string) (string, bool)           = FindJava
	_ func(string) (string, bool)           = FindDotNet
)

func TestSignatures(t *testing.T) {
	if SteamCMD(SteamApp{}, game.Dirs{}, nil) == nil {
		t.Error("SteamCMD must return an Installer")
	}
}

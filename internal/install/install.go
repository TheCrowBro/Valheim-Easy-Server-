// Package install has the generic helpers game adapters use to put a
// server on this computer: the shared SteamCMD, checked downloads,
// unpacking, the private Java and .NET runtimes, Windows prerequisites and
// the build file of non-Steam installs (spec 2.3).
//
// Each game's own version and download resolver lives in its adapter
// package, not here, so the stream that owns a game owns the facts it must
// check.
//
// Phase 0 stub: the exported signatures below are the contract the game
// streams build against; stream I fills in the bodies (and stream F adds
// fake.go). Until then every call fails with errors.ErrUnsupported, and no
// shipped code calls any of it.
package install

import (
	"context"
	"errors"

	"valheim-easy-server/internal/game"
)

// SteamCMD installs a Steam app with the shared SteamCMD in dirs.SteamCMD. Only one
// SteamCMD runs at a time in the whole app (it updates itself and keeps one package cache):
// an install or repair waits, showing "Waiting for the <game> download to finish…".
// An update-on-start never waits (see below).
func SteamCMD(app SteamApp, dirs game.Dirs, out func(string)) game.Installer {
	return unsupportedInstaller{}
}

// SteamApp is the Steam app a game's dedicated server is.
type SteamApp struct {
	AppID  string  // "1169370"
	Branch string  // "" = public
	Name   string  // "Necesse" (progress and error texts)
	SizeGB float64 // for "not enough free disk space for the Palworld server (it needs about 6 GB)"
}

// SteamBuild reads what SteamCMD installed: steamapps/appmanifest_<id>.acf.
func SteamBuild(serverDir, appID string) (game.Build, bool) {
	return game.Build{}, false
}

// Hash is a published checksum. Sources publish different ones: Mojang sha1, Adoptium
// sha256, Vintage Story md5, .NET sha512.
type Hash struct {
	Algo string // "md5", "sha1", "sha256", "sha512"; "" = none
	Hex  string
}

// Download fetches url into a temp file next to dest, checks the hash, then renames.
// Unzip/Untar refuse unsafe paths, keep or set exec bits.
func Download(ctx context.Context, url string, hash Hash, dest string, step func(game.Progress)) error {
	return errors.ErrUnsupported
}

// Unzip unpacks zipPath into dir; exec says which files must be runnable.
func Unzip(zipPath, dir string, exec func(name string) bool) error {
	return errors.ErrUnsupported
}

// Untar unpacks a .tar.gz into dir, keeping exec bits.
func Untar(tgzPath, dir string) error {
	return errors.ErrUnsupported
}

// Runtimes go into the instance's own Dirs.Runtime and are never shared, so
// FindByExe on the runtime's path finds only this instance's server. Each returns the
// path it found by searching the unpacked folder for bin/java[.exe] (on macOS
// Temurin's is under <jdk>/Contents/Home/bin/java) or dotnet[.exe]: callers use the
// returned path, never a guessed one.

// Temurin installs Adoptium's Java runtime of the given major version into dir.
func Temurin(ctx context.Context, major int, dir string, step func(game.Progress)) (java string, err error) {
	return "", errors.ErrUnsupported
}

// DotNet installs Microsoft's .NET runtime of the given channel ("8.0") into dir.
func DotNet(ctx context.Context, channel string, dir string, step func(game.Progress)) (dotnet string, err error) {
	return "", errors.ErrUnsupported
}

// Prereq is a Windows prerequisite, installed system-wide with one admin
// prompt each; the install waits for the prompt's answer (platform.RunElevatedWait).
type Prereq struct {
	Name      string      // "XNA Framework 4.0"
	Installed func() bool // registry check
	URL       string
	Hash      Hash
	Args      []string // silent install arguments
}

// EnsurePrereq installs p unless it is already installed.
func EnsurePrereq(ctx context.Context, p Prereq, step func(game.Progress)) error {
	return errors.ErrUnsupported
}

// WriteBuild records what was installed in .egs-build.json, for non-Steam installs.
func WriteBuild(serverDir string, b game.Build) error {
	return errors.ErrUnsupported
}

// ReadBuild reads .egs-build.json.
func ReadBuild(serverDir string) (game.Build, bool) {
	return game.Build{}, false
}

// unsupportedInstaller stands in until stream I writes the real SteamCMD installer.
type unsupportedInstaller struct{}

func (unsupportedInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	return errors.ErrUnsupported
}

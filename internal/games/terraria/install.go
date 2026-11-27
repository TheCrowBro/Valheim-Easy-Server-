package terraria

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// site is terraria.org, where Re-Logic publishes the dedicated server.
// Tests point it at a recorded answer.
var site = "https://terraria.org"

// The two calls the site's own download page makes (FACTS.md): the list of
// server zips, and the download of one by name.
const (
	listPath     = "/api/get/dedicated-servers-names"
	downloadPath = "/api/download/pc-dedicated-server/"
)

// reZipName is a server zip's name: "terraria-server-1461.zip" is 1.4.6.1.
var reZipName = regexp.MustCompile(`^terraria-server-(\d{2,8})\.zip$`)

// release is one server zip on terraria.org.
type release struct {
	Name    string // "terraria-server-1461.zip"
	Version string // "1.4.6.1"
}

// versionOf reads a zip name's digits as the version: one digit for each
// part ("1461" is 1.4.6.1, "145" is 1.4.5), as every release so far is
// numbered.
func versionOf(digits string) string {
	return strings.Join(strings.Split(digits, ""), ".")
}

// newer says whether version a is newer than b, part by part.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// newest picks the newest server from the site's list. The list has had
// the same name twice, and names it doesn't know are skipped.
func newest(body []byte) (release, error) {
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return release{}, fmt.Errorf("terraria.org's list of servers isn't what the app expects (%v)", err)
	}
	var best release
	for _, n := range names {
		m := reZipName.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		v := versionOf(m[1])
		if best.Name == "" || newer(v, best.Version) {
			best = release{Name: n, Version: v}
		}
	}
	if best.Name == "" {
		return release{}, errors.New("terraria.org lists no dedicated server the app can use")
	}
	return best, nil
}

// latest asks terraria.org for the newest server.
func (s *server) latest(ctx context.Context) (release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site+listPath, nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	resp, err := s.http.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("couldn't ask terraria.org for the newest server. Check the internet connection. (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("couldn't ask terraria.org for the newest server (it said %s)", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return release{}, err
	}
	return newest(body)
}

// Installer installs the server: the stand-in in fake mode, else the
// newest official server from terraria.org (and, on Windows, Microsoft's
// XNA Framework, which that server needs).
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), nil)
	}
	return installer{s}
}

type installer struct{ s *server }

// Install puts the newest server in place, or checks the one there is
// current. Repair downloads it again whatever is installed and, on
// Windows, installs XNA again over itself (installXNA).
func (i installer) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if progress == nil {
		progress = func(game.Progress) {}
	}
	s := i.s
	progress(game.Progress{Step: "Checking for the newest Terraria server…", Percent: -1})
	rel, err := s.latest(ctx)
	if err != nil {
		return err
	}
	if b, ok := s.Build(); repair || !ok || b.Version != rel.Version {
		if err := s.installRelease(ctx, rel, progress); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		if err := ensureXNA(ctx, repair, progress); err != nil {
			return err
		}
	}
	progress(game.Progress{Step: "The Terraria server " + rel.Version + " is installed.", Percent: 100})
	return nil
}

// updateOnStart installs a newer server before a start, if there is one.
// Anything that goes wrong only means the start uses the server that's
// installed (as for every game, the start never waits on a download that
// can't happen); the next start checks again.
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	b, installed := s.Build()
	step("Checking for Terraria server updates…", -1)
	rel, err := s.latest(ctx)
	if err != nil {
		s.log.Printf("couldn't check for a newer Terraria server, starting the one installed: %v", err)
		return
	}
	if installed && !newer(rel.Version, b.Version) {
		return
	}
	err = s.installRelease(ctx, rel, func(p game.Progress) { step(p.Step, p.Percent) })
	if err != nil {
		s.log.Printf("couldn't update the Terraria server to %s, starting the one installed: %v", rel.Version, err)
	}
}

// workDir is the hidden folder an install downloads and unpacks in. It is
// inside the server folder, so the finished server moves into place with a
// rename on the same drive.
const workDir = ".egs-install"

// installRelease downloads one server zip and puts this system's server in
// place.
//
// The zip holds one folder named after the version ("1461/") with a folder
// for each system ("Windows", "Mac", "Linux"). Only this system's folder is
// kept, as server/<Windows|Mac|Linux>, so the program's path stays the same
// from one version to the next and the firewall rule made for it keeps
// working after an update. The new server is unpacked completely before
// the old one is moved aside, and the old one comes back if the swap
// fails, so a failed update leaves a working server.
func (s *server) installRelease(ctx context.Context, rel release, progress func(game.Progress)) error {
	dir := s.env.Dirs.Server
	work := filepath.Join(dir, workDir)
	os.RemoveAll(work) // what an earlier install left behind
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	zipPath := filepath.Join(work, rel.Name)
	text := "Downloading the Terraria server " + rel.Version + "…"
	err := install.Download(ctx, site+downloadPath+rel.Name, install.Hash{}, zipPath, func(p game.Progress) {
		p.Step = text
		progress(p)
	})
	if err != nil {
		return err
	}
	progress(game.Progress{Step: "Unpacking the Terraria server…", Percent: -1})
	unpacked := filepath.Join(work, "unpacked")
	if err := install.Unzip(zipPath, unpacked, runnable); err != nil {
		return err
	}
	os.Remove(zipPath) // room for the swap on a full disk

	goos := runtime.GOOS
	src, err := systemFolder(unpacked, osFolder(goos))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(src, programRel(goos))); err != nil {
		return fmt.Errorf("the Terraria server %s from terraria.org has no %s for this computer", rel.Version, filepath.Base(programRel(goos)))
	}
	if err := swapIn(src, filepath.Join(dir, osFolder(goos)), filepath.Join(work, "previous")); err != nil {
		return err
	}
	if err := install.WriteBuild(dir, game.Build{Version: rel.Version, Date: time.Now().UTC().Truncate(time.Second)}); err != nil {
		return err
	}
	s.noteVersion(rel.Version)
	return nil
}

// runnable marks the files that must be runnable after unpacking: the zip
// comes from Windows and stores no Unix permissions.
func runnable(name string) bool {
	switch path.Base(name) {
	case "TerrariaServer.bin.osx", "TerrariaServer.bin.x86_64", "TerrariaServer", "Terraria", "open-folder":
		return true
	}
	return false
}

// systemFolder finds one system's folder under the zip's one top folder
// (the version's), whatever that folder is called.
func systemFolder(unpacked, system string) (string, error) {
	entries, err := os.ReadDir(unpacked)
	if err != nil {
		return "", err
	}
	var tops []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			tops = append(tops, e.Name())
		}
	}
	candidates := []string{filepath.Join(unpacked, system)} // a zip without the version folder
	for _, t := range tops {
		candidates = append(candidates, filepath.Join(unpacked, t, system))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("the Terraria server zip has no %s folder", system)
}

// swapIn moves the new folder to dest, setting the old one aside first and
// putting it back if the move fails.
func swapIn(src, dest, aside string) error {
	had := false
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, aside); err != nil {
			return fmt.Errorf("couldn't replace the Terraria server's files: is the server still running? (%v)", err)
		}
		had = true
	}
	if err := os.Rename(src, dest); err != nil {
		if had {
			os.Rename(aside, dest)
		}
		return fmt.Errorf("couldn't put the new Terraria server in place (%v)", err)
	}
	return nil
}

// ensureXNA puts XNA in place on Windows; tests of the server's install
// replace it.
var ensureXNA = installXNA

// ensurePrereq is install.EnsurePrereq; tests replace it to see which
// installer would run, and how.
var ensurePrereq = install.EnsurePrereq

// installXNA installs XNA when Windows doesn't list it, and on a repair
// installs it again over itself even when Windows does.
//
// The repair matters because of when the owner is sent to it: a start is
// refused while XNA isn't listed (Plan), so the server only ever runs, and
// ExitHint only ever says "Click Repair" over an XNA error, when Windows
// lists XNA as installed but it doesn't work (its files damaged or
// removed, with its registry key still there). The usual install would
// see the key and do nothing, and the owner would go round in a loop.
func installXNA(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if !repair || !haveXNA() {
		return ensurePrereq(ctx, xnaPrereq, progress)
	}
	if err := ensurePrereq(ctx, xnaRepairPrereq(), progress); err != nil {
		return err
	}
	// The repair runs without EnsurePrereq's own checks, so check here
	// that Windows still lists XNA afterwards.
	if !haveXNA() {
		return fmt.Errorf("The %s installer finished, but Windows doesn't list %s as installed. Try again, or restart the computer and try again.", xnaPrereq.Name, xnaPrereq.Name)
	}
	return nil
}

// xnaRepairPrereq is XNA's installer run as a repair of the XNA that is
// installed. It has no Installed check, so EnsurePrereq doesn't skip it
// because the registry key is there. And a plain "msiexec /i" of a
// package that is already installed only opens its maintenance mode and,
// run quietly, changes nothing, so these properties ask for a
// reinstall of everything in it:
//
//   - REINSTALL=ALL: every feature of the package.
//   - REINSTALLMODE=vamus: v runs from this downloaded package and caches
//     it again (the copy Windows kept may be what's damaged, and the
//     folder XNA was first installed from is long gone); a puts back every
//     file whatever its version, since a damaged file usually has the
//     same version as a good one (o, the usual choice, would skip it); m
//     and u write its registry entries again; s its shortcuts.
//
// These are only for a package that is installed: on a computer without
// it they would install nothing, so a repair there is the usual install
// (installXNA).
func xnaRepairPrereq() install.Prereq {
	p := xnaPrereq
	p.Installed = nil
	p.Args = append([]string{"REINSTALL=ALL", "REINSTALLMODE=vamus"}, xnaPrereq.Args...)
	return p
}

// xnaPrereq is Microsoft XNA Framework 4.0 Redistributable, which the
// Windows server needs (a 32-bit .NET Framework 4 program that uses XNA;
// the zip doesn't include it). The address, size and checksum are of
// Microsoft's own download, checked on 2026-11-26 (FACTS.md).
var xnaPrereq = install.Prereq{
	Name:      "Microsoft XNA Framework 4.0",
	Installed: xnaInstalled,
	URL:       "https://download.microsoft.com/download/A/C/2/AC2C903B-E6E8-42C2-9FD7-BEBAC362A930/xnafx40_redist.msi",
	Hash:      install.Hash{Algo: "sha256", Hex: "47260420773a20443fb1e38a89b7f39c5237a80a842de598e6c8f7f90a3bbd6d"},
	Args:      []string{"/quiet", "/norestart"},
}

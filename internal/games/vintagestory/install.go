package vintagestory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// Where Anego Studios lists and serves the game's files (FACTS.md). They are
// variables so the tests can point them at a recorded answer.
var (
	apiBase = "https://api.vintagestory.at"
	cdnBase = "https://cdn.vintagestory.at"
)

// defaultClient reads the version list when the instance has no client of
// its own. The list is about 100 kB, so a minute is plenty.
var defaultClient = &http.Client{Timeout: time.Minute}

// installDotNet installs the .NET runtime; the tests replace it, because
// install.DotNet always asks Microsoft's own site.
var installDotNet = install.DotNet

// fakeVersion is the version install.Fake records, so in fake mode the
// stand-in's placeholder server sits where a real version's would.
const fakeVersion = "fake"

// release is one version of the dedicated server for this computer.
type release struct {
	Version string
	URL     string
	File    string
	Hash    install.Hash
}

// serverPackage is the name stable.json gives the dedicated server for
// goos, and the file it comes in. There is no Mac server package: on a Mac
// the server only comes inside the 600 MB game (FACTS.md).
func serverPackage(goos string) (key, file string, ok bool) {
	switch goos {
	case "windows":
		return "windowsserver", "vs_server_win-x64_%s.zip", true
	case "linux":
		return "linuxserver", "vs_server_linux-x64_%s.tar.gz", true
	}
	return "", "", false
}

// reVersion is a release version, which is also a folder name here, so
// nothing else is ever taken for one: "1.22.7". Pre-releases ("1.22.0-rc.10")
// are only in unstable.json, which the app never reads.
var reVersion = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){1,3}$`)

var reMD5 = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// listedFile is one download in stable.json.
type listedFile struct {
	Filename string `json:"filename"`
	MD5      string `json:"md5"`
	URLs     struct {
		CDN   string `json:"cdn"`
		Local string `json:"local"`
	} `json:"urls"`
	Latest json.RawMessage `json:"latest"`
}

func (f listedFile) latest() bool {
	v := strings.TrimSpace(string(f.Latest))
	return v != "" && v != "0" && v != "false" && v != "null"
}

// parseListing picks the newest stable server for goos from stable.json: a
// map from version to its downloads, newest first. The newest version's
// downloads carry "latest": 1, which decides; without that mark the highest
// version number with a server for this system does.
func parseListing(b []byte, goos string) (release, error) {
	key, _, ok := serverPackage(goos)
	if !ok {
		return release{}, errNoServerHere
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return release{}, errors.New(listName + " isn't what the app expects")
	}
	var best, marked *release
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return release{}, fmt.Errorf("%s is damaged (%v)", listName, err)
		}
		version, _ := t.(string)
		var files map[string]listedFile
		if err := dec.Decode(&files); err != nil {
			return release{}, fmt.Errorf("%s is damaged (%v)", listName, err)
		}
		f, ok := files[key]
		if !ok || !reVersion.MatchString(version) || f.URLs.CDN == "" {
			continue
		}
		r := &release{Version: version, URL: f.URLs.CDN, File: f.Filename}
		if reMD5.MatchString(f.MD5) {
			r.Hash = install.Hash{Algo: "md5", Hex: strings.ToLower(f.MD5)}
		}
		if f.latest() && marked == nil {
			marked = r
		}
		if best == nil || newer(r.Version, best.Version) {
			best = r
		}
	}
	if marked != nil {
		best = marked
	}
	if best == nil {
		return release{}, errors.New(listName + " has no dedicated server for this computer")
	}
	if best.Hash.Algo == "" {
		// Every server in the list has an MD5; one without would mean the
		// list isn't what it seems, so the app doesn't install from it.
		return release{}, fmt.Errorf("%s gives no checksum for the %s server", listName, best.Version)
	}
	if err := checkLink(best.URL); err != nil {
		return release{}, err
	}
	return *best, nil
}

// newer says whether version a is after b, comparing number by number.
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

// checkLink refuses a download link that isn't HTTPS (plain HTTP only to
// this computer, which is what the tests use).
func checkLink(link string) error {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s has a download link the app can't use (%q)", listName, link)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("%s has a download link that isn't secure (%q)", listName, link)
}

// listName is how errors name stable.json.
const listName = "the version list on Vintage Story's site"

var errNoServerHere = errors.New("Vintage Story has no dedicated server download for this system: it needs Windows")

// latest asks Anego Studios for the newest stable server. If stable.json
// can't be read, lateststable.txt (just the version number) still names it,
// and the file is fetched from its usual place on the CDN without a
// checksum, as the game's own launcher would, over HTTPS.
func (s *server) latest(ctx context.Context) (release, error) {
	b, err := s.get(ctx, apiBase+"/stable.json", 4<<20)
	if err == nil {
		r, perr := parseListing(b, runtime.GOOS)
		if perr == nil || errors.Is(perr, errNoServerHere) {
			return r, perr
		}
		err = perr
	}
	_, pattern, ok := serverPackage(runtime.GOOS)
	if !ok {
		return release{}, errNoServerHere
	}
	txt, terr := s.get(ctx, apiBase+"/lateststable.txt", 64)
	v := strings.TrimSpace(string(txt))
	if terr != nil || !reVersion.MatchString(v) {
		return release{}, fmt.Errorf("Couldn't find the newest Vintage Story server: %v.", err)
	}
	file := fmt.Sprintf(pattern, v)
	s.log.Printf("stable.json failed (%v); using lateststable.txt's %s without a checksum", err, v)
	return release{Version: v, File: file, URL: cdnBase + "/gamefiles/stable/" + file}, nil
}

// get reads a small answer from Vintage Story's site.
func (s *server) get(ctx context.Context, link string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.env.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Vintage Story's site couldn't be reached (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Vintage Story's site answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("Vintage Story's site stopped answering (%v)", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("Vintage Story's site sent far more than a version list")
	}
	return b, nil
}

// Installer downloads the server and its .NET, or puts the stand-in in
// their place in fake mode.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), []string{
			filepath.Join(s.versionDir(fakeVersion), serverDLL),
			filepath.Join(s.env.Dirs.Server, ".egs-build.json"),
		})
	}
	return installer{s}
}

type installer struct{ s *server }

func (i installer) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if progress == nil {
		progress = func(game.Progress) {}
	}
	_, err := i.s.install(ctx, repair, progress)
	return err
}

// install puts the newest server and the .NET it needs in place, unless
// they are there already (a repair fetches both again). The new version is
// unpacked into a folder of its own and only becomes the installed one, in
// .egs-build.json, once its .NET is ready too; so whatever fails on the way,
// the version that was installed before still starts. It says which version
// is installed now.
func (s *server) install(ctx context.Context, repair bool, progress func(game.Progress)) (string, error) {
	step := func(text string, pct float64) { progress(game.Progress{Step: text, Percent: pct}) }
	step("Looking for the newest Vintage Story server…", -1)
	rel, err := s.latest(ctx)
	if err != nil {
		return "", err
	}
	cur, haveBuild := install.ReadBuild(s.env.Dirs.Server)
	current := haveBuild && cur.Version == rel.Version && isFile(filepath.Join(s.versionDir(rel.Version), serverDLL))
	if repair || !current {
		if err := s.fetchServer(ctx, rel, progress); err != nil {
			return "", err
		}
	}
	if err := s.ensureDotNet(ctx, rel.Version, repair, progress); err != nil {
		return "", err
	}
	if !current || repair {
		if err := install.WriteBuild(s.env.Dirs.Server, game.Build{Version: rel.Version, Date: time.Now().UTC().Truncate(time.Second)}); err != nil {
			return "", err
		}
		s.removeOldVersions(rel.Version)
	}
	s.setVersion(rel.Version)
	step("The Vintage Story server "+rel.Version+" is installed.", 100)
	return rel.Version, nil
}

// fetchServer downloads one version's zip, checks it, and unpacks it into
// its own folder (the zip has no folder at the top, FACTS.md).
func (s *server) fetchServer(ctx context.Context, rel release, progress func(game.Progress)) error {
	dl := filepath.Join(s.env.Dirs.Server, ".download")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return err
	}
	name := filepath.Base(rel.File)
	if _, pattern, ok := serverPackage(runtime.GOOS); ok && (name == "." || name == "" || strings.ContainsAny(name, `/\:`)) {
		name = fmt.Sprintf(pattern, rel.Version) // the name it always has on the CDN
	}
	archive := filepath.Join(dl, name)
	defer func() {
		os.Remove(archive)
		os.Remove(dl)
	}()
	text := "Downloading the Vintage Story server " + rel.Version + "…"
	err := install.Download(ctx, rel.URL, rel.Hash, archive, func(p game.Progress) {
		p.Step = text
		progress(p)
	})
	if err != nil {
		return err
	}
	progress(game.Progress{Step: "Unpacking the Vintage Story server…", Percent: -1})
	final := s.versionDir(rel.Version)
	partial := final + ".partial"
	os.RemoveAll(partial)
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		err = install.Unzip(archive, partial, func(n string) bool { return n == "VintagestoryServer" || strings.HasSuffix(n, ".sh") })
	} else {
		err = install.Untar(archive, partial)
	}
	if err != nil {
		os.RemoveAll(partial)
		return err
	}
	if !isFile(filepath.Join(partial, serverDLL)) {
		os.RemoveAll(partial)
		return fmt.Errorf("the Vintage Story %s download doesn't have %s in it", rel.Version, serverDLL)
	}
	// A repair of the same version replaces its folder: the old one is set
	// aside first and put back if the new one can't take its place.
	old := final + ".old"
	os.RemoveAll(old)
	hadOld := os.Rename(final, old) == nil
	if err := os.Rename(partial, final); err != nil {
		if hadOld {
			os.Rename(old, final)
		}
		os.RemoveAll(partial)
		return fmt.Errorf("couldn't put the new server files in place: %w", err)
	}
	os.RemoveAll(old)
	return nil
}

// removeOldVersions removes the folders of other versions and the leftovers
// of broken installs. A file still in use (Windows) keeps its folder until
// the next install, which does no harm.
func (s *server) removeOldVersions(keep string) {
	entries, err := os.ReadDir(s.env.Dirs.Server)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		base := strings.TrimSuffix(strings.TrimSuffix(n, ".partial"), ".old")
		if !e.IsDir() || n == keep || (!reVersion.MatchString(base) && base != fakeVersion) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.env.Dirs.Server, n)); err != nil {
			s.log.Printf("couldn't remove the old server folder %s yet: %v", n, err)
		}
	}
}

// dotnetChannel reads which .NET the server needs from its
// runtimeconfig.json: Microsoft.NETCore.App "10.0.0" is channel "10.0".
func dotnetChannel(versionDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(versionDir, "VintagestoryServer.runtimeconfig.json"))
	if err != nil {
		return "", fmt.Errorf("the server's files don't say which .NET it needs (%v)", err)
	}
	type fw struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	var rc struct {
		RuntimeOptions struct {
			Framework  *fw  `json:"framework"`
			Frameworks []fw `json:"frameworks"`
		} `json:"runtimeOptions"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\ufeff")), &rc); err != nil {
		return "", fmt.Errorf("the server's runtimeconfig.json can't be read (%v)", err)
	}
	all := rc.RuntimeOptions.Frameworks
	if rc.RuntimeOptions.Framework != nil {
		all = append(all, *rc.RuntimeOptions.Framework)
	}
	for _, f := range all {
		if f.Name != "Microsoft.NETCore.App" {
			continue
		}
		parts := strings.Split(f.Version, ".")
		if len(parts) >= 2 && reVersion.MatchString(parts[0]+"."+parts[1]) {
			return parts[0] + "." + parts[1], nil
		}
	}
	return "", errors.New("the server's runtimeconfig.json doesn't name the .NET it needs")
}

// ensureDotNet makes sure the private .NET can run this version. The
// installed one stays as long as it has the channel the server asks for:
// install.DotNet keeps each .NET version in a folder of its own, so a newer
// patch would move Program() and, with it, the firewall rule's program,
// which would need the owner's admin prompt again before friends could
// connect. A repair always fetches the newest patch.
func (s *server) ensureDotNet(ctx context.Context, version string, repair bool, progress func(game.Progress)) error {
	channel, err := dotnetChannel(s.versionDir(version))
	if err != nil {
		return err
	}
	// A repair sets the old .NET aside in one move first (install.DotNet
	// keeps a version that's already there), and puts it back if the new one
	// can't be had. A server still running from it keeps Windows from moving
	// it, and then nothing has changed.
	aside := ""
	if !repair {
		if dotnet, ok := install.FindDotNet(s.env.Dirs.Runtime); ok && hasRuntime(dotnet, channel) {
			return nil
		}
	} else {
		aside = s.env.Dirs.Runtime + ".old"
		os.RemoveAll(aside)
		if err := os.Rename(s.env.Dirs.Runtime, aside); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("couldn't replace the old .NET (is the server still running?): %w", err)
			}
			aside = ""
		}
	}
	putBack := func() {
		if aside != "" {
			os.RemoveAll(s.env.Dirs.Runtime)
			os.Rename(aside, s.env.Dirs.Runtime)
		}
	}
	if err := os.MkdirAll(s.env.Dirs.Runtime, 0o755); err != nil {
		putBack()
		return err
	}
	dotnet, err := installDotNet(ctx, channel, s.env.Dirs.Runtime, progress)
	if err == nil && !hasRuntime(dotnet, channel) {
		err = fmt.Errorf("the .NET that was installed doesn't have the %s runtime the server needs", channel)
	}
	if err != nil {
		putBack()
		return err
	}
	if aside != "" {
		os.RemoveAll(aside)
	}
	return nil
}

// hasRuntime says whether the dotnet at path has a Microsoft.NETCore.App of
// channel ("10.0"), in .NET's own layout: shared/Microsoft.NETCore.App/10.0.15.
func hasRuntime(dotnet, channel string) bool {
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(dotnet), "shared", "Microsoft.NETCore.App"))
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool {
		return e.IsDir() && strings.HasPrefix(e.Name(), channel+".")
	})
}

// updateOnStart fetches a newer server before a start, if there is one. If
// that fails the start goes on with the version that's installed: a server
// a version behind beats no server (friends whose game updated itself are
// told the version on the Join card).
func (s *server) updateOnStart(ctx context.Context, step func(string, float64)) {
	step("Checking for Vintage Story server updates…", -1)
	_, err := s.install(ctx, false, func(p game.Progress) { step(p.Step, p.Percent) })
	if err != nil && ctx.Err() == nil {
		step("Couldn't check for server updates ("+strings.TrimSuffix(err.Error(), ".")+"). Starting the version that's installed.", -1)
		s.log.Printf("update on start failed: %v", err)
	}
}

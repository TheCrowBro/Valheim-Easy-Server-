// Package selfupdate keeps the app up to date from the project's GitHub
// releases. Each build that passes its tests is published as a release
// tagged build-N, with the program and its SHA-256 checksum.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Where the releases are.
const (
	Repo       = "TheCrowBro/Valheim-Easy-Server-"
	DefaultAPI = "https://api.github.com"
	ExeName    = "ValheimEasyServer.exe"
	maxExe     = 200 << 20
)

// Release is a published build.
type Release struct {
	Build     int       `json:"build"`
	Title     string    `json:"title"` // what changed: the first line of its notes
	Published time.Time `json:"published"`
	exeURL    string
	sumURL    string
}

// Updater finds and downloads new builds.
type Updater struct {
	API    string       // GitHub's API; DefaultAPI when empty
	Repo   string       // owner/name; Repo when empty
	Client *http.Client // one with a timeout when nil
}

var reBuild = regexp.MustCompile(`^build-(\d+)$`)

// Latest returns the newest build that has the program and its checksum;
// ok is false when there's none.
func (u *Updater) Latest(ctx context.Context) (rel Release, ok bool, err error) {
	var list []struct {
		Tag        string    `json:"tag_name"`
		Body       string    `json:"body"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
		Published  time.Time `json:"published_at"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	res, err := u.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=30", u.api(), u.repo()), "application/vnd.github+json")
	if err != nil {
		return rel, false, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&list); err != nil {
		return rel, false, fmt.Errorf("GitHub's answer couldn't be read: %v", err)
	}
	for _, r := range list {
		m := reBuild.FindStringSubmatch(r.Tag)
		if m == nil || r.Draft || r.Prerelease {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		c := Release{Build: n, Title: firstLine(r.Body), Published: r.Published}
		for _, a := range r.Assets {
			switch a.Name {
			case ExeName:
				c.exeURL = a.URL
			case ExeName + ".sha256":
				c.sumURL = a.URL
			}
		}
		if c.exeURL != "" && c.sumURL != "" && (!ok || n > rel.Build) {
			rel, ok = c, true
		}
	}
	return rel, ok, nil
}

// Download fetches the release's program to path, checks it against the
// published checksum, and removes it again if it doesn't match.
func (u *Updater) Download(ctx context.Context, rel Release, path string) error {
	for _, link := range []string{rel.exeURL, rel.sumURL} {
		if !strings.HasPrefix(strings.ToLower(link), strings.ToLower(u.downloads())) {
			return fmt.Errorf("unexpected download address %q", link)
		}
	}
	res, err := u.get(ctx, rel.sumURL, "application/octet-stream")
	if err != nil {
		return err
	}
	line, _ := bufio.NewReader(io.LimitReader(res.Body, 1024)).ReadString('\n')
	res.Body.Close()
	want := strings.ToLower(strings.TrimSpace(strings.Fields(line + " x")[0]))
	if len(want) != 64 {
		return errors.New("the release's checksum file isn't valid")
	}

	res, err = u.get(ctx, rel.exeURL, "application/octet-stream")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxExe+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("the download broke off: %v", err)
	case n > maxExe:
		err = errors.New("the download is too big")
	case hex.EncodeToString(h.Sum(nil)) != want:
		err = errors.New("the download doesn't match the release's checksum")
	case !isProgram(path):
		err = errors.New("the download isn't a Windows program")
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// isProgram checks for the "MZ" every Windows program starts with.
func isProgram(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 2)
	_, err = io.ReadFull(f, b)
	return err == nil && string(b) == "MZ"
}

// Swap puts the downloaded program in place of the running one. Windows
// lets a running program be renamed, so it carries on as exe.old until it
// exits. That copy stays until the next update, so the previous version
// can be put back.
func Swap(exe, downloaded string) error {
	old := exe + ".old"
	os.Remove(old)
	if err := retry(func() error { return os.Rename(exe, old) }); err != nil {
		return err
	}
	if err := retry(func() error { return os.Rename(downloaded, exe) }); err != nil {
		os.Rename(old, exe) // put it back
		return err
	}
	return nil
}

// retry tries fn a few times: an antivirus may hold a new file for a moment.
func retry(fn func() error) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

// Restore puts the previous version back after an update that didn't start.
func Restore(exe string) error {
	return retry(func() error { return os.Rename(exe+".old", exe) })
}

// CleanUp removes a download that an update didn't finish.
func CleanUp(exe string) {
	os.Remove(exe + ".new")
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func (u *Updater) api() string {
	if u.API != "" {
		return strings.TrimRight(u.API, "/")
	}
	return DefaultAPI
}

func (u *Updater) repo() string {
	if u.Repo != "" {
		return u.Repo
	}
	return Repo
}

// downloads is where the release files must be: GitHub's release downloads
// for the repository (they redirect to GitHub's file servers), or the test
// server in tests.
func (u *Updater) downloads() string {
	if u.API != "" && u.API != DefaultAPI {
		return strings.TrimRight(u.API, "/") + "/"
	}
	return "https://github.com/" + u.repo() + "/releases/download/"
}

func (u *Updater) get(ctx context.Context, link, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	req.Header.Set("Accept", accept)
	client := u.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("couldn't reach GitHub. Is the internet connection working?")
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	return res, nil
}

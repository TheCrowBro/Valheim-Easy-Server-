// Package steamcmd installs and updates the Valheim dedicated server with
// Valve's SteamCMD tool, reporting progress as it goes.
package steamcmd

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/platform"
)

// AppID is the Steam app ID of the Valheim dedicated server. It is free and
// can be downloaded with an anonymous login.
const AppID = "896660"

// downloads is where SteamCMD comes from on each system.
var downloads = map[string]string{
	"windows": "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip",
	"darwin":  "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_osx.tar.gz",
	"linux":   "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz",
}

// Progress is reported while installing.
type Progress struct {
	Step    string  // what is happening, in plain words
	Percent float64 // 0..100, or -1 when unknown
}

// Installer installs or updates the server files.
type Installer interface {
	// Install downloads or updates the server. validate re-checks every
	// file, which is slower but repairs a broken install.
	Install(ctx context.Context, validate bool, progress func(Progress)) error
}

// SteamCMD is the real installer.
type SteamCMD struct {
	Dir        string       // where steamcmd lives
	InstallDir string       // where the server goes
	Output     func(string) // receives raw SteamCMD output, may be nil
}

func (s *SteamCMD) exe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(s.Dir, "steamcmd.exe")
	}
	return filepath.Join(s.Dir, "steamcmd.sh")
}

// Install implements Installer.
func (s *SteamCMD) Install(ctx context.Context, validate bool, progress func(Progress)) error {
	if err := s.ensureSteamCMD(ctx, progress); err != nil {
		return err
	}
	if err := os.MkdirAll(s.InstallDir, 0o755); err != nil {
		return err
	}
	installDir, err := filepath.Abs(s.InstallDir)
	if err != nil {
		return err
	}
	args := []string{"+force_install_dir", installDir, "+login", "anonymous", "+app_update", AppID}
	if validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")

	// SteamCMD sometimes needs a second run right after updating itself.
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		progress(Progress{Step: "Connecting to Steam…", Percent: -1})
		lastErr = s.run(ctx, args, progress)
		if lastErr == nil || ctx.Err() != nil {
			break
		}
		if s.Output != nil {
			s.Output(fmt.Sprintf("SteamCMD attempt %d failed: %v", attempt, lastErr))
		}
	}
	if ctx.Err() != nil {
		return errors.New("cancelled")
	}
	return lastErr
}

func (s *SteamCMD) ensureSteamCMD(ctx context.Context, progress func(Progress)) error {
	if _, err := os.Stat(s.exe()); err == nil {
		return nil
	}
	link, ok := downloads[runtime.GOOS]
	if !ok {
		return fmt.Errorf("SteamCMD isn't available for %s", runtime.GOOS)
	}
	progress(Progress{Step: "Downloading SteamCMD…", Percent: 0})
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't download SteamCMD. Check the internet connection. (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't download SteamCMD (server said %s)", resp.Status)
	}
	archive := filepath.Join(s.Dir, filepath.Base(link))
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, report: func(pct float64) {
		progress(Progress{Step: "Downloading SteamCMD…", Percent: pct})
	}}
	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		return fmt.Errorf("couldn't download SteamCMD: %v", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return extractOne(archive, "steamcmd.exe", s.exe())
	}
	// The macOS one has a framework with links inside: the system's tar
	// unpacks it as intended.
	if out, err := exec.CommandContext(ctx, "tar", "-xzf", archive, "-C", s.Dir).CombinedOutput(); err != nil {
		return fmt.Errorf("the SteamCMD download couldn't be unpacked: %v %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(s.exe()); err != nil {
		return errors.New("the SteamCMD download doesn't contain steamcmd.sh")
	}
	return nil
}

// extractOne copies a single named file out of a zip archive.
func extractOne(zipPath, name, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("the SteamCMD download is damaged: %v", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if !strings.EqualFold(filepath.Base(zf.Name), name) || zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(rc, 64<<20)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("the SteamCMD download doesn't contain %s", name)
}

var (
	reUpdateState = regexp.MustCompile(`Update state \(0x([0-9a-fA-F]+)\) ([^,]+), progress: ([\d.]+)`)
	reSelfUpdate  = regexp.MustCompile(`^\[\s*(\d+)%\]\s*(.*)`)
	reSuccess     = regexp.MustCompile(`(?i)^Success! App '` + AppID + `'`)
	reError       = regexp.MustCompile(`(?i)^(?:ERROR!|Error!)\s*(.*)`)
)

// run starts SteamCMD and follows its output. Lines can end in \r or \n.
func (s *SteamCMD) run(ctx context.Context, args []string, progress func(Progress)) error {
	cmd := exec.CommandContext(ctx, s.exe(), args...)
	cmd.Dir = s.Dir
	cmd.SysProcAttr = platform.HiddenProcAttr()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := platform.StartCmd(cmd); err != nil {
		return fmt.Errorf("couldn't start SteamCMD: %v", err)
	}

	var (
		success  bool
		failMsg  string
		lastLine string
	)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if s.Output != nil {
			s.Output(line)
		}
		lastLine = line
		switch {
		case reSuccess.MatchString(line):
			success = true
			progress(Progress{Step: "Server files are up to date.", Percent: 100})
		case reError.MatchString(line):
			failMsg = FriendlyError(reError.FindStringSubmatch(line)[1])
		default:
			if p, ok := ParseProgress(line); ok {
				progress(p)
			}
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if success {
		return nil
	}
	if failMsg != "" {
		return errors.New(failMsg)
	}
	if waitErr != nil {
		return fmt.Errorf("SteamCMD stopped unexpectedly (%v). Last message: %s", waitErr, lastLine)
	}
	return fmt.Errorf("SteamCMD finished without confirming the install. Last message: %s", lastLine)
}

// ParseProgress turns one SteamCMD output line into a progress update.
func ParseProgress(line string) (Progress, bool) {
	if m := reUpdateState.FindStringSubmatch(line); m != nil {
		pct, _ := strconv.ParseFloat(m[3], 64)
		state := strings.ToLower(strings.TrimSpace(m[2]))
		step := "Working…"
		switch {
		case strings.Contains(state, "download"):
			step = "Downloading the Valheim server…"
		case strings.Contains(state, "verif"):
			step = "Checking the server files…"
		case strings.Contains(state, "prealloc"):
			step = "Making room on the disk…"
		case strings.Contains(state, "commit"):
			step = "Finishing the install…"
		case strings.Contains(state, "reconfig"):
			step = "Configuring…"
		}
		return Progress{Step: step, Percent: pct}, true
	}
	if m := reSelfUpdate.FindStringSubmatch(line); m != nil {
		pct, _ := strconv.ParseFloat(m[1], 64)
		return Progress{Step: "Updating SteamCMD…", Percent: pct}, true
	}
	switch {
	case strings.HasPrefix(line, "Connecting anonymously"), strings.HasPrefix(line, "Logging in"):
		return Progress{Step: "Connecting to Steam…", Percent: -1}, true
	case strings.HasPrefix(line, "Waiting for user info"), strings.HasPrefix(line, "Waiting for client config"):
		return Progress{Step: "Talking to Steam…", Percent: -1}, true
	}
	return Progress{}, false
}

// FriendlyError explains common SteamCMD errors in plain words.
func FriendlyError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "0x202"), strings.Contains(low, "disk space"):
		return "There isn't enough free disk space for the Valheim server (it needs about 2 GB)."
	case strings.Contains(low, "disk write"), strings.Contains(low, "0x602"):
		return "SteamCMD couldn't write the server files. Make sure the disk isn't full and the server isn't running."
	case strings.Contains(low, "timeout"), strings.Contains(low, "no connection"), strings.Contains(low, "connect"):
		return "SteamCMD couldn't reach Steam. Check the internet connection and try again."
	case strings.Contains(low, "bad cpu type"):
		return "SteamCMD needs Rosetta on a Mac with Apple silicon. Open Terminal, run: softwareupdate --install-rosetta, then try again."
	case strings.Contains(low, "0x6"):
		return "SteamCMD hit an error while updating (" + msg + "). Try again, or use Repair."
	}
	return "SteamCMD reported an error: " + msg
}

// scanLinesCR splits on \n, \r\n or a bare \r (SteamCMD redraws lines).
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			j := i + 1
			if b == '\r' && j < len(data) && data[j] == '\n' {
				j++
			}
			return j, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

type progressReader struct {
	r       io.Reader
	total   int64
	read    int64
	lastPct float64
	report  func(float64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		pct := float64(p.read) * 100 / float64(p.total)
		if pct-p.lastPct >= 2 || pct >= 100 {
			p.lastPct = pct
			p.report(pct)
		}
	}
	return n, err
}

var reBuildID = regexp.MustCompile(`"buildid"\s+"(\d+)"`)
var reLastUpdated = regexp.MustCompile(`"LastUpdated"\s+"(\d+)"`)

// InstalledBuild reads the Steam build ID and update time of the server.
func InstalledBuild(installDir string) (buildID string, updated time.Time, ok bool) {
	b, err := os.ReadFile(filepath.Join(installDir, "steamapps", "appmanifest_"+AppID+".acf"))
	if err != nil {
		return "", time.Time{}, false
	}
	if m := reBuildID.FindSubmatch(b); m != nil {
		buildID = string(m[1])
	}
	if m := reLastUpdated.FindSubmatch(b); m != nil {
		if sec, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
			updated = time.Unix(sec, 0)
		}
	}
	return buildID, updated, buildID != ""
}

// SetInstallDir changes where the server is installed.
func (s *SteamCMD) SetInstallDir(dir string) { s.InstallDir = dir }

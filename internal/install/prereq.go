package install

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// Prereq is a Windows prerequisite, installed system-wide with one admin
// prompt each; the install waits for the prompt's answer (platform.RunElevatedWait).
type Prereq struct {
	Name      string      // "XNA Framework 4.0"
	Installed func() bool // registry check
	URL       string
	Hash      Hash
	Args      []string // silent install arguments
}

// runElevated runs an installer as administrator and waits for it. Tests
// replace it.
var runElevated = platform.RunElevatedWait

// Installer exit codes that mean it worked: 0; 3010 and 1641, installed
// but Windows wants a restart (the server runs without one); 1638, a newer
// version is already installed (the Visual C++ runtime says so).
var installedCodes = map[int]bool{0: true, 3010: true, 1641: true, 1638: true}

// errorCancelled is Windows' ERROR_CANCELLED: the owner said No to the
// administrator prompt.
const errorCancelled = 1223

// EnsurePrereq installs p unless it is already installed.
//
// The installer is downloaded to a temporary folder, checked against p.Hash
// and run as administrator with p.Args. A .msi is run through Windows'
// msiexec as "msiexec /i <file> <Args…>". Afterwards p.Installed must say
// yes, so an installer that quietly did nothing isn't taken for success.
func EnsurePrereq(ctx context.Context, p Prereq, step func(game.Progress)) error {
	if step == nil {
		step = func(game.Progress) {}
	}
	if p.Installed != nil && p.Installed() {
		return nil
	}
	if p.URL == "" {
		return fmt.Errorf("%s isn't installed, and the app doesn't know where to get it", p.Name)
	}
	tmp, err := os.MkdirTemp("", "egs-prereq-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, installerName(p.URL))
	text := "Downloading " + p.Name + "…"
	err = Download(ctx, p.URL, p.Hash, file, func(pr game.Progress) {
		pr.Step = text
		step(pr)
	})
	if err != nil {
		return err
	}

	step(game.Progress{Step: "Installing " + p.Name + "… Click Yes when Windows asks for permission.", Percent: -1})
	exe, args := file, p.Args
	if strings.EqualFold(filepath.Ext(file), ".msi") {
		exe, args = msiexec(), append([]string{"/i", file}, p.Args...)
	}
	code, err := runElevated(ctx, exe, args)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, errors.ErrUnsupported):
		return fmt.Errorf("%s isn't installed, and the app can't install it on this computer.", p.Name)
	case err != nil && (code == errorCancelled || errors.Is(err, syscall.Errno(errorCancelled))):
		return fmt.Errorf("%s wasn't installed: it needs you to click Yes when Windows asks for permission. Try again.", p.Name)
	case err != nil:
		return fmt.Errorf("Couldn't install %s (%v).", p.Name, err)
	case code == errorCancelled:
		return fmt.Errorf("%s wasn't installed: it needs you to click Yes when Windows asks for permission. Try again.", p.Name)
	case !installedCodes[code]:
		return fmt.Errorf("The %s installer failed (exit code %d).", p.Name, code)
	}
	if p.Installed != nil && !p.Installed() {
		return fmt.Errorf("The %s installer finished, but Windows doesn't list %s as installed. Try again, or restart the computer and try again.", p.Name, p.Name)
	}
	step(game.Progress{Step: p.Name + " is installed.", Percent: 100})
	return nil
}

var reInstallerName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// installerName is the installer's file name from its link
// ("xnafx40_redist.msi", "vc_redist.x64.exe"): Windows decides how to run a
// file by its extension.
func installerName(link string) string {
	if u, err := url.Parse(link); err == nil {
		if name := path.Base(u.Path); reInstallerName.MatchString(name) && strings.Contains(name, ".") {
			return name
		}
	}
	return "setup.exe"
}

// msiexec is Windows' own msiexec, by its full path, so a file of that
// name anywhere else is never run as administrator by mistake.
func msiexec() string {
	if runtime.GOOS == "windows" {
		if root := os.Getenv("SystemRoot"); root != "" {
			return filepath.Join(root, "System32", "msiexec.exe")
		}
	}
	return "msiexec.exe"
}

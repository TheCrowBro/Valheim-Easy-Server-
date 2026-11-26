package install

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"valheim-easy-server/internal/game"
)

// elevatedCall is one pretend run as administrator.
type elevatedCall struct {
	exe  string
	args []string
	body string // the installer's contents, read while it ran
}

// fakeElevated stands in for the administrator prompt and the installer.
func fakeElevated(t *testing.T, code int, err error, then func()) *[]elevatedCall {
	t.Helper()
	var calls []elevatedCall
	old := runElevated
	runElevated = func(ctx context.Context, exe string, args []string) (int, error) {
		file := exe
		if strings.HasSuffix(strings.ToLower(exe), "msiexec.exe") && len(args) > 1 {
			file = args[1]
		}
		b, _ := os.ReadFile(file)
		calls = append(calls, elevatedCall{exe: exe, args: args, body: string(b)})
		if then != nil {
			then()
		}
		return code, err
	}
	t.Cleanup(func() { runElevated = old })
	return &calls
}

func installerSite(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("installer for " + r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestEnsurePrereqAlreadyInstalled(t *testing.T) {
	srv, hits := installerSite(t)
	calls := fakeElevated(t, 0, nil, nil)
	p := Prereq{Name: "XNA Framework 4.0", Installed: func() bool { return true }, URL: srv.URL + "/xnafx40_redist.msi"}
	if err := EnsurePrereq(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 || len(*calls) != 0 {
		t.Error("an installed prerequisite was downloaded or run")
	}
}

func TestEnsurePrereqExe(t *testing.T) {
	srv, _ := installerSite(t)
	installed := false
	calls := fakeElevated(t, 3010, nil, func() { installed = true }) // 3010: done, restart wanted
	body := "installer for /vs/17/release/vc_redist.x64.exe"
	p := Prereq{
		Name:      "Visual C++ Redistributable",
		Installed: func() bool { return installed },
		URL:       srv.URL + "/vs/17/release/vc_redist.x64.exe",
		Hash:      Hash{Algo: "sha256", Hex: sums([]byte(body))["sha256"]},
		Args:      []string{"/install", "/quiet", "/norestart"},
	}
	var steps []string
	if err := EnsurePrereq(context.Background(), p, func(pr game.Progress) { steps = append(steps, pr.Step) }); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
	c := (*calls)[0]
	if filepath.Base(c.exe) != "vc_redist.x64.exe" || !reflect.DeepEqual(c.args, p.Args) || c.body != body {
		t.Errorf("ran %s %q (%q)", c.exe, c.args, c.body)
	}
	if _, err := os.Stat(c.exe); !os.IsNotExist(err) {
		t.Error("the installer was left behind")
	}
	joined := strings.Join(steps, "\n")
	for _, want := range []string{"Downloading Visual C++ Redistributable…", "Installing Visual C++ Redistributable… Click Yes when Windows asks for permission.", "Visual C++ Redistributable is installed."} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress %q lacks %q", steps, want)
		}
	}
}

// A .msi is run through Windows' own msiexec.
func TestEnsurePrereqMSI(t *testing.T) {
	srv, _ := installerSite(t)
	installed := false
	calls := fakeElevated(t, 0, nil, func() { installed = true })
	p := Prereq{Name: "XNA Framework 4.0", Installed: func() bool { return installed },
		URL: srv.URL + "/download/A/C/2/xnafx40_redist.msi", Args: []string{"/quiet"}}
	if err := EnsurePrereq(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if !strings.EqualFold(filepath.Base(c.exe), "msiexec.exe") || len(c.args) != 3 || c.args[0] != "/i" ||
		filepath.Base(c.args[1]) != "xnafx40_redist.msi" || c.args[2] != "/quiet" {
		t.Errorf("ran %s %q", c.exe, c.args)
	}
	if c.body != "installer for /download/A/C/2/xnafx40_redist.msi" {
		t.Errorf("the msi held %q", c.body)
	}
}

func TestEnsurePrereqFailures(t *testing.T) {
	srv, _ := installerSite(t)
	never := func() bool { return false }
	base := Prereq{Name: "XNA Framework 4.0", Installed: never, URL: srv.URL + "/xnafx40_redist.msi"}
	for _, c := range []struct {
		code int
		err  error
		want string
	}{
		{1223, errors.New("The operation was canceled by the user."), "XNA Framework 4.0 wasn't installed: it needs you to click Yes when Windows asks for permission. Try again."},
		{1223, nil, "XNA Framework 4.0 wasn't installed: it needs you to click Yes when Windows asks for permission. Try again."},
		{-1, fmt.Errorf("ShellExecuteEx: %w", syscall.Errno(1223)), "XNA Framework 4.0 wasn't installed: it needs you to click Yes when Windows asks for permission. Try again."},
		{1603, nil, "The XNA Framework 4.0 installer failed (exit code 1603)."},
		{0, nil, "The XNA Framework 4.0 installer finished, but Windows doesn't list XNA Framework 4.0 as installed. Try again, or restart the computer and try again."},
		{-1, errors.ErrUnsupported, "XNA Framework 4.0 isn't installed, and the app can't install it on this computer."},
		{-1, errors.New("boom"), "Couldn't install XNA Framework 4.0 (boom)."},
	} {
		fakeElevated(t, c.code, c.err, nil)
		err := EnsurePrereq(context.Background(), base, nil)
		if err == nil || err.Error() != c.want {
			t.Errorf("code %d, %v: %v, want %q", c.code, c.err, err, c.want)
		}
	}

	// A damaged download is never run as administrator.
	calls := fakeElevated(t, 0, nil, nil)
	bad := base
	bad.Hash = Hash{Algo: "sha256", Hex: strings.Repeat("0", 64)}
	if err := EnsurePrereq(context.Background(), bad, nil); !errors.Is(err, errChecksum) {
		t.Errorf("EnsurePrereq with a wrong checksum = %v", err)
	}
	if len(*calls) != 0 {
		t.Error("a damaged installer was run")
	}
	noURL := base
	noURL.URL = ""
	if err := EnsurePrereq(context.Background(), noURL, nil); err == nil {
		t.Error("EnsurePrereq without a link worked")
	}
}

func TestInstallerName(t *testing.T) {
	for link, want := range map[string]string{
		"https://aka.ms/vs/17/release/vc_redist.x64.exe":                                                        "vc_redist.x64.exe",
		"https://download.microsoft.com/download/A/C/2/AC2C903B-E6E8-42C2-9FD7-BEBAC362A930/xnafx40_redist.msi": "xnafx40_redist.msi",
		"https://example.com/get?file=x":                                                                        "setup.exe",
		"https://example.com/":                                                                                  "setup.exe",
		"https://example.com/bad%20name.exe":                                                                    "setup.exe",
	} {
		if got := installerName(link); got != want {
			t.Errorf("installerName(%s) = %s, want %s", link, got, want)
		}
	}
}

//go:build windows

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows lays SHELLEXECUTEINFOW out in 112 bytes on 64-bit Windows and
// 60 on 32-bit; RunElevatedWait's copy must match.
func TestShellExecuteInfoSize(t *testing.T) {
	want := map[string]uintptr{"amd64": 112, "arm64": 112, "386": 60, "arm": 60}
	if w, ok := want[runtime.GOARCH]; ok && unsafe.Sizeof(shellExecuteInfo{}) != w {
		t.Fatalf("SHELLEXECUTEINFOW is %d bytes here, want %d", unsafe.Sizeof(shellExecuteInfo{}), w)
	}
	if off := unsafe.Offsetof(shellExecuteInfo{}.hProcess); runtime.GOARCH == "amd64" && off != 104 {
		t.Fatalf("hProcess at %d, want 104", off)
	}
}

// With admin rights (the Windows CI runner), RunElevatedWait runs a
// program as admin without a prompt, waits for it and gives its exit code,
// with arguments that need quoting arriving whole.
func TestRunElevatedWaitReal(t *testing.T) {
	needCIAdmin(t, "RunElevatedWait runs programs as admin and gives their exit codes")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	code, err := RunElevatedWait(ctx, system32("cmd.exe"), []string{"/c", "exit 7"})
	if err != nil || code != 7 {
		t.Fatalf("RunElevatedWait = %d, %v; want 7", code, err)
	}
	marker := filepath.Join(t.TempDir(), "with spaces", "marker.txt")
	os.MkdirAll(filepath.Dir(marker), 0o755)
	code, err = RunElevatedWait(ctx, system32(`WindowsPowerShell\v1.0\powershell.exe`),
		[]string{"-NoProfile", "-NonInteractive", "-Command", "Set-Content -LiteralPath " + psQuote(marker) + " -Value 'it ran'"})
	if err != nil || code != 0 {
		t.Fatalf("RunElevatedWait = %d, %v; want 0", code, err)
	}
	if b, _ := os.ReadFile(marker); !strings.Contains(string(b), "it ran") {
		t.Fatalf("marker %q", b)
	}
}

// markerScript is a PowerShell script that writes "it ran" to marker,
// padded with a comment, when long, so it is too long for the admin
// prompt's command line and has to go through the checked temp file.
func markerScript(marker string, long bool) string {
	script := "Set-Content -LiteralPath " + psQuote(marker) + " -Value 'it ran'"
	if long {
		script += "\n# " + strings.Repeat("padding so it goes through the checked temp file ", 60)
	}
	return script
}

// wantRoute fails unless psAdminCommand sends script through the temp
// file exactly when it should (viaFile).
func wantRoute(t *testing.T, exe, script string, viaFile bool) {
	t.Helper()
	saved := false
	if _, err := psAdminCommand(exe, script, func([]byte) (string, error) {
		saved = true
		return `C:\Users\Someone\AppData\Local\Temp\ValheimEasyServer-admin-1.ps1`, nil
	}); err != nil {
		t.Fatal(err)
	}
	if saved != viaFile {
		t.Fatalf("through the temp file: %v, want %v", saved, viaFile)
	}
}

// The checked temp file's short command works in Windows PowerShell 5.1,
// the PowerShell the app runs as admin, with exactly the command line the
// admin prompt is given. TestAdminCommandRunsOnlyTheSavedScript checks it
// with PowerShell 7, which isn't the same program. This needs no admin
// rights, so it runs on every Windows test run.
func TestAdminCommandWindowsPowerShell(t *testing.T) {
	exe := system32(`WindowsPowerShell\v1.0\powershell.exe`)
	marker := filepath.Join(t.TempDir(), "with spaces", "marker.txt")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	script := markerScript(marker, true)
	wantRoute(t, exe, script, true)
	var path string
	args, err := psAdminCommand(exe, script, func(b []byte) (string, error) {
		p, err := writeAdminScript(b)
		path = p
		return p, err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	run := func() (string, error) {
		cmd := exec.Command(exe)
		// The command line exactly as the admin prompt gets it, in a
		// console of its own, so -WindowStyle Hidden hides nothing else.
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW, CmdLine: `"` + exe + `" ` + args}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(); err != nil {
		t.Fatalf("Windows PowerShell didn't run the saved script: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(marker); !strings.Contains(string(b), "it ran") {
		t.Fatalf("the saved script didn't run: marker %q", b)
	}
	os.Remove(marker)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\nSet-Content -LiteralPath " + psQuote(marker) + " -Value 'changed'\n")
	f.Close()
	if out, err := run(); err == nil {
		t.Fatalf("a changed script ran without complaint:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a changed script ran")
	}
}

// With admin rights (the Windows CI runner), runElevatedPowerShell, which
// FixFirewall and FixFirewallAll use, runs a script as admin: a short one
// in the command line, as FixFirewall's usually goes, and one too long for
// it through the checked temp file, as FixFirewallAll's goes once games
// are added. When TestFixFirewallAllReal finds no rules at all, this says
// whether the script ran.
func TestRunElevatedPowerShellReal(t *testing.T) {
	needCIAdmin(t, "runElevatedPowerShell runs short and long scripts as admin")
	exe := system32(`WindowsPowerShell\v1.0\powershell.exe`)
	for _, long := range []bool{false, true} {
		name := map[bool]string{false: "short", true: "long"}[long]
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "with spaces", "marker.txt")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			script := markerScript(marker, long)
			wantRoute(t, exe, script, long)
			if err := runElevatedPowerShell(script); err != nil {
				t.Fatal(err)
			}
			// It returns once Windows has been asked; the script runs a
			// moment later.
			for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
				if b, _ := os.ReadFile(marker); strings.Contains(string(b), "it ran") {
					return
				}
			}
			t.Fatalf("the %s script didn't run as admin within 60 s", name)
		})
	}
}

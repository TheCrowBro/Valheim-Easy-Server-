//go:build windows

package platform

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
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
	if os.Getenv("EGS_CI_ADMIN") != "1" {
		t.Skip("set EGS_CI_ADMIN=1 on a computer where admin rights need no prompt (the CI runner)")
	}
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

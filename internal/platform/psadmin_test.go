package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodeAdminArgs gives back the script in psAdminArgs's command line.
func decodeAdminArgs(t *testing.T, args string) string {
	t.Helper()
	_, enc, ok := strings.Cut(args, "-EncodedCommand ")
	if !ok {
		t.Fatalf("no encoded command in %q", args)
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

const testPowerShell = `C:\WINDOWS\system32\WindowsPowerShell\v1.0\powershell.exe`

// A short script goes in the command line, as before.
func TestAdminCommandShortScript(t *testing.T) {
	script := firewallScript(`C:\Valheim Server\server\valheim_server.exe`, `C:\Valheim Server\ValheimEasyServer.exe`, true)
	args, err := psAdminCommand(testPowerShell, script, func([]byte) (string, error) {
		t.Fatal("a short script was saved to a file")
		return "", nil
	})
	if err != nil || decodeAdminArgs(t, args) != script {
		t.Fatalf("args %q, %v", args, err)
	}
}

// However long the folder paths, Windows can still ask for admin
// permission: the script goes in a file that is checked before it runs.
func TestAdminCommandLongPaths(t *testing.T) {
	deep := `C:\Users\Astrid Lothbrok PC\OneDrive - Contoso Corporation Limited\Documents\Games and Servers\Valheim\Friends Server 2026\ValheimEasyServer-windows`
	script := firewallScript(deep+`\server\valheim_server.exe`, deep+`\ValheimEasyServer.exe`, true)
	if len(`"`+testPowerShell+`" `+psAdminArgs(script)) < shellExecuteMax {
		t.Fatal("this script is meant to be too long for the command line")
	}
	var saved []byte
	path := `C:\Users\Astrid Lothbrok PC\AppData\Local\Temp\ValheimEasyServer-admin-123456789.ps1`
	args, err := psAdminCommand(testPowerShell, script, func(b []byte) (string, error) { saved = b; return path, nil })
	if err != nil {
		t.Fatal(err)
	}
	if n := len(`"` + testPowerShell + `" ` + args); n >= shellExecuteMax {
		t.Fatalf("the command line is %d characters", n)
	}
	boot := decodeAdminArgs(t, args)
	if string(saved) != script || !strings.Contains(boot, psQuote(path)) || !strings.Contains(boot, fmt.Sprintf("%x", sha256.Sum256(saved))) {
		t.Fatalf("saved %q\nbootstrap %q", saved, boot)
	}
}

// With PowerShell at hand, the short command really runs the saved script,
// and refuses one that was changed after the app saved it.
func TestAdminCommandRunsOnlyTheSavedScript(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell (pwsh) isn't installed")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	script := "Set-Content -LiteralPath " + psQuote(marker) + " -Value 'it ran'\n# " + strings.Repeat("padding so it doesn't fit in the command line ", 40)
	var path string
	args, err := psAdminCommand(testPowerShell, script, func(b []byte) (string, error) {
		path = filepath.Join(dir, "admin.ps1")
		return path, os.WriteFile(path, b, 0o600)
	})
	if err != nil || path == "" {
		t.Fatalf("not saved: %v", err)
	}
	_, enc, _ := strings.Cut(args, "-EncodedCommand ")
	run := func() error { return exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-EncodedCommand", enc).Run() }
	if err := run(); err != nil {
		t.Fatalf("the saved script didn't run: %v", err)
	}
	if b, _ := os.ReadFile(marker); !strings.Contains(string(b), "it ran") {
		t.Fatalf("marker %q", b)
	}
	os.Remove(marker)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\nSet-Content -LiteralPath " + psQuote(marker) + " -Value 'changed'\n")
	f.Close()
	if err := run(); err == nil {
		t.Fatal("a changed script ran without complaint")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a changed script ran")
	}
}

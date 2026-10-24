package platform

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPSQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Valheim Server\server\valheim_server.exe`: `'C:\Valheim Server\server\valheim_server.exe'`,
		`C:\Users\O'Brien\x.exe`:                      `'C:\Users\O''Brien\x.exe'`,
		`C:\Bjørn’s $(evil) & 100%\x.exe`:             `'C:\Bjørn’’s $(evil) & 100%\x.exe'`,
	} {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPSEncode(t *testing.T) {
	// What PowerShell's own [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes(...)) gives.
	if got := psEncode("Write-Output 'hi'"); got != "VwByAGkAdABlAC0ATwB1AHQAcAB1AHQAIAAnAGgAaQAnAA==" {
		t.Errorf("psEncode = %q", got)
	}
}

// The quoted path comes back unchanged from PowerShell itself (Windows
// PowerShell on Windows, as the app uses; pwsh elsewhere when installed).
func TestPSRoundTrip(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		if ps, err = exec.LookPath("pwsh"); err != nil {
			t.Skip("no PowerShell here")
		}
	}
	path := `C:\Bjørn’s server ‘x’ $(whoami) & 100% "a" ;b\valheim_server.exe`
	// PowerShell compares, so the console's code page doesn't matter.
	var codes []string
	for _, c := range utf16.Encode([]rune(path)) {
		codes = append(codes, strconv.Itoa(int(c)))
	}
	script := "$p = " + psQuote(path) + "\n$want = -join [char[]]@(" + strings.Join(codes, ",") + ")\n" +
		"if ($p -ceq $want) { 'same' } else { 'different' }"
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(script)).Output()
	if err != nil {
		t.Fatalf("%s: %v", ps, err)
	}
	if got := strings.TrimSpace(string(out)); got != "same" {
		t.Fatalf("PowerShell's copy of the path is %q", got)
	}
}

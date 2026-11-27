//go:build windows

package vintagestory

import (
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// getOEMCP is kernel32's GetOEMCP, which x/sys/windows doesn't wrap. It is
// looked up only from Windows' own folder.
var getOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// oemCodePage is the code page a new console on this computer starts with
// (437 on a US Windows, 850 in much of western Europe, 65001 with Windows'
// "use UTF-8" option), so the one the server's hidden console writes in;
// 0 when Windows doesn't say.
func oemCodePage() uint32 {
	if getOEMCP.Find() != nil {
		return 0
	}
	cp, _, _ := getOEMCP.Call()
	return uint32(cp)
}

// consoleText is a line of the server's output as text.
//
// On Windows .NET writes a program's redirected output in its console's
// code page (Console.OutputEncoding is GetConsoleOutputCP's), and the
// hidden console each game gets starts with the computer's OEM code page.
// So a player or world name with a letter outside ASCII would reach the log
// in that code page ("Jürgen" as "J\x81rgen" with code page 437), not in
// UTF-8, and show up garbled on the Log page and in the players list. A
// line that is valid UTF-8 (plain ASCII, or a console that writes UTF-8)
// is kept as it is; any other is read in the OEM code page. The log file
// itself keeps the bytes the server wrote. The Windows real-server cell
// shows which bytes it writes (FACTS.md, "The console on Windows").
func consoleText(raw string) string {
	if utf8.ValidString(raw) {
		return raw
	}
	if s, ok := fromCodePage(oemCodePage(), raw); ok {
		return s
	}
	return raw
}

// fromCodePage reads s as text in the Windows code page cp. It says false
// when Windows can't (no code page, or one it doesn't know).
func fromCodePage(cp uint32, s string) (string, bool) {
	if cp == 0 || s == "" {
		return "", false
	}
	b := []byte(s)
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n <= 0 {
		return "", false
	}
	w := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &w[0], n)
	if err != nil || n <= 0 {
		return "", false
	}
	return string(utf16.Decode(w[:n])), true
}

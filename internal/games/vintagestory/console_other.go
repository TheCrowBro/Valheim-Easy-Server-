//go:build !windows

package vintagestory

// consoleText is a line of the server's output as text. On Linux and the
// Mac .NET writes its console output in UTF-8, so the line is kept as it
// is (see console_windows.go for Windows, where it isn't).
func consoleText(raw string) string { return raw }

// oemCodePage is Windows' console code page; there is none here.
func oemCodePage() uint32 { return 0 }

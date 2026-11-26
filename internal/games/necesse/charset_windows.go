//go:build windows

package necesse

import (
	"strconv"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WideCharToMultiByte turns Unicode into a code page (golang.org/x/sys
// has its opposite, MultiByteToWideChar, but not this one).
var procWideCharToMultiByte = windows.NewLazySystemDLL("kernel32.dll").NewProc("WideCharToMultiByte")

const (
	cpUTF8 = 65001
	// wcNoBestFitChars stops Windows from swapping a missing character
	// for a look-alike ("ł" for "l"). Java's launcher converts its command
	// line with this flag too (FACTS.md), so a missing character becomes
	// "?" there; and a look-alike wouldn't be what the owner typed anyway.
	wcNoBestFitChars = 0x400
)

// argRuneFits says whether a character reaches the server's Java as it is
// on its command line: whether this computer's ANSI code page, which
// Java's launcher converts the command line into (FACTS.md), has it. With
// the code page set to UTF-8 ("Use Unicode UTF-8 for worldwide language
// support" in Windows' region settings) every character does. Tests
// replace it.
var argRuneFits = func(r rune) bool {
	acp := windows.GetACP()
	if acp == cpUTF8 {
		return true
	}
	u := utf16.Encode([]rune{r})
	var buf [16]byte
	var usedDefault int32 // a Windows BOOL: set when the character was missing
	n, _, _ := procWideCharToMultiByte.Call(uintptr(acp), wcNoBestFitChars,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		0, uintptr(unsafe.Pointer(&usedDefault)))
	// n is 0 when Windows couldn't convert it at all: missing too.
	return n > 0 && usedDefault == 0
}

// codePage names the character set Java gets its arguments in, for the
// real-server check's notes.
func codePage() string { return "code page " + strconv.Itoa(int(windows.GetACP())) }

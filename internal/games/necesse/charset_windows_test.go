//go:build windows

package necesse

import (
	"testing"

	"golang.org/x/sys/windows"
)

// The code page check against this Windows computer's own code page (the
// CI runner's), so the call into Windows itself is tested.
func TestArgRuneFitsHere(t *testing.T) {
	acp := windows.GetACP()
	t.Logf("this computer's %s", codePage())
	for _, r := range "Az09 -_.,'#!()" {
		if !argRuneFits(r) {
			t.Errorf("%q doesn't fit code page %d: plain ASCII is in every code page", r, acp)
		}
	}
	if acp == cpUTF8 {
		if !argRuneFits('🏡') {
			t.Errorf("with the UTF-8 code page every character fits")
		}
		return
	}
	if argRuneFits('🏡') {
		t.Errorf("an emoji fits code page %d", acp)
	}
	if acp == 1252 {
		// "é" is in Windows-1252; "ł" isn't, and Windows' look-alike
		// ("l") must not count.
		if !argRuneFits('é') || argRuneFits('ł') {
			t.Errorf("code page 1252: é fits %v (want true), ł fits %v (want false)", argRuneFits('é'), argRuneFits('ł'))
		}
	}
}

package mods

import (
	"bytes"
	"embed"
	"os"
	"path/filepath"
)

//go:embed dist
var dist embed.FS

// doorstopMac is the macOS Doorstop, which loads BepInEx (and so the mods)
// into the server. The release build carries one built for macOS 11 and
// later: BepInExPack_Valheim's own is built for the newest macOS only, so
// an older Mac, like a 2013 Mac Pro on Monterey, can't load it.
const doorstopMac = "dist/libdoorstop.dylib"

// macDoorstop puts the app's Doorstop next to the server, if this build
// carries one, and returns its path; otherwise it's BepInExPack's.
func macDoorstop(dir string) string {
	pack := filepath.Join(dir, "doorstop_libs", "libdoorstop_x64.dylib")
	b, err := dist.ReadFile(doorstopMac)
	if err != nil {
		return pack
	}
	path := filepath.Join(dir, "doorstop_libs", "libdoorstop_valheimeasyserver.dylib")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, b) {
		return path
	}
	tmp := path + ".tmp"
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil || os.WriteFile(tmp, b, 0o755) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return pack
	}
	return path
}

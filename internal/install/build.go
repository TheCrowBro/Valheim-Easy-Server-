package install

import (
	"encoding/json"
	"os"
	"path/filepath"

	"valheim-easy-server/internal/game"
)

// buildFile is where a non-Steam install notes what it installed (SteamCMD
// keeps its own appmanifest instead).
const buildFile = ".egs-build.json"

// WriteBuild records what was installed in .egs-build.json, for non-Steam installs.
// It is written whole or not at all, so a crash mid-write never leaves a
// build that reads as half of one.
func WriteBuild(serverDir string, b game.Build) error {
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(serverDir, buildFile), b)
}

// ReadBuild reads .egs-build.json. It says false when there is none, or it
// can't be read, or it names no version or build.
func ReadBuild(serverDir string) (game.Build, bool) {
	data, err := os.ReadFile(filepath.Join(serverDir, buildFile))
	if err != nil {
		return game.Build{}, false
	}
	var b game.Build
	if json.Unmarshal(data, &b) != nil || (b.Version == "" && b.Build == "") {
		return game.Build{}, false
	}
	return b, true
}

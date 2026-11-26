package terraria

import (
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worldstore"
)

// worldExts are the files of one world: the world, the copy of the save
// before it, and the rolling backups the server keeps beside them (one by
// default, up to nine with worldrollbackstokeep; FACTS.md).
var worldExts = []string{".wld", ".wld.bak", ".wld.bak2", ".wld.bak3", ".wld.bak4", ".wld.bak5",
	".wld.bak6", ".wld.bak7", ".wld.bak8", ".wld.bak9"}

// maxWorldName keeps a world's name, which is also its file's name, short
// enough for every system. The server itself takes at least 40 characters.
const maxWorldName = 40

// steamAppID is the Terraria game's id on Steam, whose cloud saves keep
// worlds under userdata/<account>/105600/remote/worlds.
const steamAppID = "105600"

// worldFormat is how Terraria keeps its worlds: a world is <name>.wld and
// the backups beside it, all in one folder.
func worldFormat(saves string) worldstore.Format {
	return worldstore.Format{
		Dir:     saves,
		Kind:    worldstore.KindFiles,
		Ext:     worldExts,
		MaxName: maxWorldName,
		LocalIn: localWorlds,
	}
}

// localWorlds are the folders where the owner's own Terraria keeps its
// worlds on this computer: its Documents folder on Windows (which OneDrive
// may have moved, so it asks Windows where Documents is), Application
// Support on a Mac, and Steam's cloud folders for worlds saved to the
// cloud.
func localWorlds(home string) []string {
	var out []string
	switch runtime.GOOS {
	case "windows":
		if docs := platform.KnownFolder("documents"); docs != "" {
			out = append(out, filepath.Join(docs, "My Games", "Terraria", "Worlds"))
		}
		if home != "" {
			out = append(out, filepath.Join(home, "Documents", "My Games", "Terraria", "Worlds"))
		}
	case "darwin":
		if home != "" {
			out = append(out, filepath.Join(home, "Library", "Application Support", "Terraria", "Worlds"))
		}
	default:
		if home != "" {
			out = append(out, filepath.Join(home, ".local", "share", "Terraria", "Worlds"))
		}
	}
	if steam := platform.SteamPath(); steam != "" {
		if cloud, err := filepath.Glob(filepath.Join(steam, "userdata", "*", steamAppID, "remote", "worlds")); err == nil {
			out = append(out, cloud...)
		}
	}
	return out
}

// worldStore is the world library, with Terraria's one rule on top: a
// world can't be renamed, because the name players see is kept inside the
// world file, and the file's new name would no longer match it.
type worldStore struct{ game.Worlds }

func (w worldStore) Rename(name, newName string) error { return game.ErrCantRename }

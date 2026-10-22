package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ListKind names one of Valheim's permission files in the save folder.
type ListKind string

const (
	Admins    ListKind = "admins"
	Banned    ListKind = "banned"
	Permitted ListKind = "permitted"
)

// ListKinds is every list, in display order.
var ListKinds = []ListKind{Admins, Banned, Permitted}

// FileName is the file Valheim reads for this list.
func (k ListKind) FileName() string {
	switch k {
	case Admins:
		return "adminlist.txt"
	case Banned:
		return "bannedlist.txt"
	case Permitted:
		return "permittedlist.txt"
	}
	return ""
}

func (k ListKind) comment() string {
	switch k {
	case Admins:
		return "// List admin players ID  ONE per line"
	case Banned:
		return "// List banned players ID  ONE per line"
	default:
		return "// List permitted players ID ONE per line"
	}
}

var playerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,80}$`)

// ReadList returns the player IDs in a list file (comments skipped).
func ReadList(saveDir string, kind ListKind) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(saveDir, kind.FileName()))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		ids = append(ids, line)
	}
	return ids, sc.Err()
}

// CleanIDs trims, de-duplicates and checks player IDs.
func CleanIDs(ids []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if !playerIDPattern.MatchString(id) {
			return nil, fmt.Errorf("%q doesn't look like a player ID (for example Steam_76561198000000000).", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// WriteList replaces a list file with ids.
func WriteList(saveDir string, kind ListKind, ids []string) error {
	ids, err := CleanIDs(ids)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(kind.comment())
	b.WriteString("\n")
	for _, id := range ids {
		b.WriteString(id)
		b.WriteString("\n")
	}
	return WriteFileAtomic(filepath.Join(saveDir, kind.FileName()), []byte(b.String()), 0o644)
}

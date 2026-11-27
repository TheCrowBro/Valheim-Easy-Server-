package minecraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// The three lists are Minecraft's own files in the server folder. While
// the server runs they change through its own commands over RCON, so the
// server never overwrites a change the app made to the file behind its
// back; while it's stopped the app writes the files itself, as Minecraft
// writes them.
type listSpec struct {
	kind         game.ListKind
	file         string
	add, remove  string // RCON commands, "%s" = the name
	extra        func(now time.Time) map[string]any
	addOK, remOK []string // replies that mean done (or nothing to do)
}

var lists = []listSpec{
	{
		kind: game.ListKind{ID: "allowed", Label: "Allow list", Placeholder: "Minecraft name", Live: true,
			Help: "While the allow list is on, only these players can join."},
		file: "whitelist.json", add: "whitelist add %s", remove: "whitelist remove %s",
		addOK: []string{"Added ", "Player is already whitelisted"}, remOK: []string{"Removed ", "Player is not whitelisted"},
	},
	{
		kind: game.ListKind{ID: "admins", Label: "Admins", Placeholder: "Minecraft name", Live: true,
			Help: "Admins (operators) can use every command in the game, such as /gamemode, /kick and /ban."},
		file: "ops.json", add: "op %s", remove: "deop %s",
		extra: func(time.Time) map[string]any { return map[string]any{"level": 4, "bypassesPlayerLimit": false} },
		addOK: []string{"Made ", "Nothing changed. The player is already an operator"}, remOK: []string{"Made ", "Nothing changed. The player is not an operator"},
	},
	{
		kind: game.ListKind{ID: "banned", Label: "Banned", Placeholder: "Minecraft name", Live: true,
			Help: "Banned players can't join, even when they're on the allow list."},
		file: "banned-players.json", add: "ban %s", remove: "pardon %s",
		extra: func(now time.Time) map[string]any {
			return map[string]any{"created": now.UTC().Format("2006-01-02 15:04:05 -0700"), "source": "Server", "expires": "forever", "reason": "Banned by an operator."}
		},
		addOK: []string{"Banned ", "Nothing changed. The player is already banned"}, remOK: []string{"Unbanned ", "Nothing changed. The player isn't banned"},
	},
}

func listByID(kind string) (listSpec, bool) {
	for _, l := range lists {
		if l.kind.ID == kind {
			return l, true
		}
	}
	return listSpec{}, false
}

// Lists are the allow list, the admins and the bans.
func (s *server) Lists() []game.ListKind {
	out := make([]game.ListKind, len(lists))
	for i, l := range lists {
		out[i] = l.kind
	}
	return out
}

// reName is a Minecraft account name: 3 to 16 letters, digits and
// underscores for every account made today; a few very old ones are
// shorter, so 1 is allowed.
var reName = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// entry is one player in a list file, with every field the file had, so
// rewriting it keeps what Minecraft or the owner put there.
type entry map[string]any

func (e entry) str(key string) string { s, _ := e[key].(string); return s }

// readEntries reads a list file; a missing file is an empty list.
func readEntries(path string) ([]entry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []entry
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s isn't a list Minecraft wrote (%v)", filepath.Base(path), err)
	}
	return out, nil
}

// writeEntries writes a list file the way Minecraft does (two-space
// indent), whole or not at all.
func writeEntries(path string, list []entry) error {
	if list == nil {
		list = []entry{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o644)
}

func (s *server) listPath(file string) string { return filepath.Join(s.env.Dirs.Server, file) }

// ReadList returns the names on a list.
func (s *server) ReadList(kind string) ([]string, error) {
	l, ok := listByID(kind)
	if !ok {
		return nil, fmt.Errorf("Minecraft has no list called %q", kind)
	}
	entries, err := readEntries(s.listPath(l.file))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if n := e.str("name"); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// WriteList makes the list exactly names. With the server running (rec
// set) the changes go through RCON, so they apply at once and Minecraft
// looks each name up itself; stopped, the file is written with each
// player's account id, looked up the way the server would.
func (s *server) WriteList(ctx context.Context, rec *game.Record, kind string, names []string) error {
	l, ok := listByID(kind)
	if !ok {
		return fmt.Errorf("Minecraft has no list called %q", kind)
	}
	want, err := cleanNames(names)
	if err != nil {
		return err
	}
	have, err := s.ReadList(kind)
	if err != nil {
		return err
	}
	add, remove := diffNames(have, want)
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	if rec != nil {
		return s.writeLive(ctx, rec, l, add, remove)
	}
	return s.writeFile(ctx, l, add, remove)
}

// cleanNames checks the names and drops repeats (names are the same in
// any letter case for Minecraft).
func cleanNames(names []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !reName.MatchString(n) {
			return nil, fmt.Errorf("%q isn't a Minecraft name: names have only letters, digits and _ (up to 16).", n)
		}
		if k := strings.ToLower(n); !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// diffNames says who to add and who to take off, ignoring letter case.
func diffNames(have, want []string) (add, remove []string) {
	in := func(list []string, n string) bool {
		for _, x := range list {
			if strings.EqualFold(x, n) {
				return true
			}
		}
		return false
	}
	for _, n := range want {
		if !in(have, n) {
			add = append(add, n)
		}
	}
	for _, n := range have {
		if !in(want, n) {
			remove = append(remove, n)
		}
	}
	return add, remove
}

// rconAddr is where the running server's RCON answers, from its record.
func rconAddr(rec *game.Record) (addr, password string, err error) {
	port := rec.Ports[roleRCON]
	if port == 0 {
		return "", "", errors.New("the run record has no RCON port")
	}
	return net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), rec.Secrets[roleRCON], nil
}

// writeLive sends the changes over RCON.
func (s *server) writeLive(ctx context.Context, rec *game.Record, l listSpec, add, remove []string) error {
	addr, pw, err := rconAddr(rec)
	if err != nil {
		return err
	}
	c, err := rcon.Dial(ctx, addr, pw)
	if err != nil {
		return fmt.Errorf("Minecraft isn't answering the app yet (%v). Try again in a moment.", err)
	}
	defer c.Close()
	var problems []string
	run := func(format, name string, ok []string) {
		reply, err := c.Exec(ctx, fmt.Sprintf(format, name))
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
		case strings.Contains(reply, "That player does not exist"):
			problems = append(problems, fmt.Sprintf("Minecraft doesn't know a player called %s; check the spelling.", name))
		case !hasAnyPrefix(reply, ok):
			problems = append(problems, fmt.Sprintf("%s: Minecraft said %q", name, reply))
		}
	}
	for _, n := range add {
		run(l.add, n, l.addOK)
	}
	for _, n := range remove {
		run(l.remove, n, l.remOK)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, " "))
	}
	return nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// writeFile changes the list file while the server is stopped.
func (s *server) writeFile(ctx context.Context, l listSpec, add, remove []string) error {
	path := s.listPath(l.file)
	entries, err := readEntries(path)
	if err != nil {
		return err
	}
	var kept []entry
	for _, e := range entries {
		drop := false
		for _, n := range remove {
			if strings.EqualFold(e.str("name"), n) {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, e)
		}
	}
	now := time.Now()
	for _, n := range add {
		p, err := s.lookupPlayer(ctx, n)
		if err != nil {
			return err
		}
		e := entry{"uuid": p.uuid, "name": p.name}
		if l.extra != nil {
			for k, v := range l.extra(now) {
				e[k] = v
			}
		}
		kept = append(kept, e)
	}
	return writeEntries(path, kept)
}

// player is a Minecraft account.
type player struct{ name, uuid string }

// mojangProfiles answers which account a name belongs to. Tests point it
// at recorded answers.
var mojangProfiles = "https://api.mojang.com/users/profiles/minecraft/"

// lookupPlayer finds a name's account id: from the players this server has
// seen (its user cache and its lists), else from Mojang, as the server's
// own "whitelist add" does.
func (s *server) lookupPlayer(ctx context.Context, name string) (player, error) {
	for _, p := range s.knownPlayers() {
		if strings.EqualFold(p.name, name) {
			return p, nil
		}
	}
	return mojangLookup(ctx, s.env.HTTP, name)
}

// mojangLookup asks Mojang's profile API. It answers {"id": 32 hex digits,
// "name": "Notch"}, or 404 for a name no account has; list files need the
// id with dashes (8-4-4-4-12).
func mojangLookup(ctx context.Context, client *http.Client, name string) (player, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mojangProfiles+url.PathEscape(name), nil)
	if err != nil {
		return player{}, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	resp, err := client.Do(req)
	if err != nil {
		return player{}, fmt.Errorf("Couldn't ask Mojang who %s is. Check the internet connection, or add them while the server runs. (%v)", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusNoContent:
		return player{}, fmt.Errorf("Minecraft doesn't know a player called %s; check the spelling.", name)
	default:
		return player{}, fmt.Errorf("Mojang couldn't say who %s is right now (it answered %s). Try again later.", name, resp.Status)
	}
	var p struct{ ID, Name string }
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return player{}, fmt.Errorf("Mojang's answer about %s wasn't understood (%v).", name, err)
	}
	id, ok := dashUUID(p.ID)
	if !ok {
		return player{}, fmt.Errorf("Mojang gave %q as %s's account, which isn't an account id.", p.ID, name)
	}
	if p.Name == "" {
		p.Name = name
	}
	return player{name: p.Name, uuid: id}, nil
}

var reHex32 = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// dashUUID writes a 32-digit account id as 8-4-4-4-12, the way the list
// files hold it. An id that already has its dashes is kept.
func dashUUID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if reUUID.MatchString(id) {
		return id, true
	}
	if !reHex32.MatchString(id) {
		return "", false
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], true
}

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// knownPlayers are the accounts this server has seen: Minecraft's user
// cache (in 26.3 it sits in the worlds folder, --universe; older versions
// kept it in the server folder) and its three lists.
func (s *server) knownPlayers() []player {
	var out []player
	seen := map[string]bool{}
	add := func(name, uuid string) {
		id, ok := dashUUID(uuid)
		if name == "" || !ok || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		out = append(out, player{name: name, uuid: id})
	}
	for _, path := range []string{
		filepath.Join(s.env.Dirs.Saves, "usercache.json"),
		filepath.Join(s.env.Dirs.Server, "usercache.json"),
		s.listPath("whitelist.json"), s.listPath("ops.json"), s.listPath("banned-players.json"),
	} {
		entries, _ := readEntries(path)
		for _, e := range entries {
			add(e.str("name"), e.str("uuid"))
		}
	}
	return out
}

// cachedPlayers are the players in Minecraft's user cache only: everyone
// who has joined this server (and names the server has looked up).
func (s *server) cachedPlayers() []player {
	var out []player
	for _, path := range []string{filepath.Join(s.env.Dirs.Saves, "usercache.json"), filepath.Join(s.env.Dirs.Server, "usercache.json")} {
		entries, _ := readEntries(path)
		for _, e := range entries {
			if id, ok := dashUUID(e.str("uuid")); ok && e.str("name") != "" {
				out = append(out, player{name: e.str("name"), uuid: id})
			}
		}
	}
	return out
}

// writeFileAtomic writes a file through a temporary one beside it, so a
// crash never leaves half a file for the server to read.
func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

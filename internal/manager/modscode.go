package manager

// The mods share code: friends import it in r2modman or Gale to get every
// mod the running world uses, at the server's versions, in one step. It's
// made when the server starts and posted in Discord with the join code.

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/mods"
)

// codeWaitMods is the longest the Discord post waits for the mods code.
var codeWaitMods = 45 * time.Second

// savedCode is a share code made earlier, kept in modscodes.json so the same
// mods at the same versions keep the same code while Thunderstore has it.
type savedCode struct {
	Code string    `json:"code"`
	Made time.Time `json:"made"`
}

// makeModsCode gets the running world's mods code in the background, then
// lets the Discord post go out, with the code if there is one.
func (m *Manager) makeModsCode(run *serverRun, ids []string, world string, p config.Paths) {
	code := ""
	defer func() { // whatever happens, the post doesn't wait any longer
		m.mu.Lock()
		run.info.codePending = false
		if code != "" {
			run.info.ModsCode = code
		}
		post, announce := m.announceLocked(run)
		m.mu.Unlock()
		if announce {
			m.queueDiscord(post)
		}
		m.saveRunInfo(run)
		m.notify()
	}()
	ctx, cancel := context.WithTimeout(m.ctx, codeWaitMods)
	defer cancel()
	var err error
	if code, err = m.modsCode(ctx, ids, world, p); err != nil {
		m.log.Printf("mods share code: %v", err)
	}
}

// modsCode returns the share code for the world's mods: the one made
// before for the same mods and versions while Thunderstore still has it,
// or a new one. It's empty for a world without mods.
func (m *Manager) modsCode(ctx context.Context, ids []string, world string, p config.Paths) (string, error) {
	in := &mods.Installer{Dir: p.Server, URL: m.opt.ModsURL}
	pkgs, err := in.ProfilePackages(ctx, ids)
	if err != nil || len(pkgs) == 0 {
		return "", err
	}
	name := mods.ProfileName(world)
	parts := []string{name}
	for _, pk := range pkgs {
		parts = append(parts, pk.Name+"-"+pk.Version)
	}
	key := strings.Join(parts, "|")

	m.codesMu.Lock()
	defer m.codesMu.Unlock()
	path := m.dataFile("modscodes.json")
	codes := map[string]savedCode{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &codes)
	}
	if c, ok := codes[key]; ok {
		// When Thunderstore can't be asked, the code most likely still works.
		if works, err := in.CodeWorks(ctx, c.Code); works || err != nil {
			return c.Code, nil
		}
	}
	profile, err := mods.Profile(name, pkgs)
	if err != nil {
		return "", err
	}
	code, err := in.ShareCode(ctx, profile)
	if err != nil {
		return "", err
	}
	codes[key] = savedCode{Code: code, Made: time.Now()}
	if len(codes) > 20 { // keep the newest
		keys := make([]string, 0, len(codes))
		for k := range codes {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return codes[b].Made.Compare(codes[a].Made) })
		for _, k := range keys[20:] {
			delete(codes, k)
		}
	}
	if b, err := json.MarshalIndent(codes, "", "  "); err == nil {
		if err := writeFileAtomic(path, b, 0o644); err != nil {
			m.log.Printf("saving modscodes.json: %v", err)
		}
	}
	return code, nil
}

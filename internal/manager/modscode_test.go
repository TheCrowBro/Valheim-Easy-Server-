package manager

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/mods/modstest"
)

// The world's mods go into a profile on Thunderstore whose code friends
// import in r2modman or Gale; the code is posted with the join code, kept
// while it works, and made again once Thunderstore forgets it.
func TestModsCode(t *testing.T) {
	fake := modstest.New()
	store := httptest.NewServer(fake)
	defer store.Close()
	chat := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.ModsURL, m.opt.DiscordURL = store.URL, chat.URL
	install(t, m)
	turnOnDiscord(t, m)
	world := m.Settings().World
	if err := m.SetWorldMods(world, []string{"advancedportals", "farlands"}); err != nil {
		t.Fatal(err)
	}

	join := startOnline(t, m)
	chat.wait(t, "Join code: **"+join+"**", 1)
	profiles := fake.Profiles()
	if len(profiles) != 1 {
		t.Fatalf("%d profiles uploaded", len(profiles))
	}
	var code string
	for k := range profiles {
		code = k
	}
	post := chat.all()[len(chat.all())-1]
	if !strings.Contains(post, "📦 Mods code: `"+code+"`") || !strings.Contains(post, "import the mods code as a new profile") {
		t.Fatalf("the post has no mods code (it waits for it):\n%s", post)
	}
	if st := m.Status(); st.ModsCode != code {
		t.Fatalf("status mods code %q, want %q", st.ModsCode, code)
	}
	list := profileList(t, profiles[code])
	for _, want := range []string{`profileName: "Valheim ` + world + `"`, "name: denikson-BepInExPack_Valheim", "name: RandyKnapp-AdvancedPortals",
		"name: ValheimModding-Jotunn", "name: Ivvty-FarLands"} {
		if !strings.Contains(list, want) {
			t.Fatalf("the profile lacks %q:\n%s", want, list)
		}
	}

	// The code is for the mods the server started with: while the world's
	// mods are different, there's none to show.
	if err := m.SetWorldMods(world, []string{"advancedportals"}); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.ModsCode != "" {
		t.Fatalf("a code for other mods: %q", st.ModsCode)
	}
	if err := m.SetWorldMods(world, []string{"farlands", "advancedportals"}); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.ModsCode != code {
		t.Fatalf("the code is back as %q, want %q", st.ModsCode, code)
	}

	restart := func() {
		t.Helper()
		if err := m.RestartServer(); err != nil {
			t.Fatal(err)
		}
		if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
			t.Fatalf("restart: %s", job.Error)
		}
		waitFor(t, "the mods code", 20*time.Second, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.run != nil && !m.run.info.codePending
		})
	}
	// The same mods keep the same code.
	restart()
	if st := m.Status(); st.ModsCode != code || len(fake.Profiles()) != 1 {
		t.Fatalf("code %q after a restart (want %q), %d profiles", st.ModsCode, code, len(fake.Profiles()))
	}
	// Once Thunderstore forgets it, there's a new one.
	fake.Expire(code)
	restart()
	if st := m.Status(); st.ModsCode == "" || st.ModsCode == code || len(fake.Profiles()) != 1 {
		t.Fatalf("code %q after the old one expired, %d profiles", st.ModsCode, len(fake.Profiles()))
	}
	// A world without mods has none.
	if err := m.SetWorldMods(world, nil); err != nil {
		t.Fatal(err)
	}
	restart()
	if st := m.Status(); st.ModsCode != "" {
		t.Fatalf("a mods code without mods: %q", st.ModsCode)
	}
}

// profileList is the list of mods in a profile, as a mod manager reads it.
func profileList(t *testing.T, profile string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(profile, "#r2modman\n"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := zr.Open("export.r2x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	y, _ := io.ReadAll(f)
	return string(y)
}

// If the app closes while the code is still being made, the online post
// isn't lost: the app that takes over the server makes the code and posts.
func TestModsCodeWhenTheAppCloses(t *testing.T) {
	fake := modstest.New()
	store := httptest.NewServer(fake)
	defer store.Close()
	chat := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.ModsURL, m.opt.DiscordURL = store.URL, chat.URL
	install(t, m)
	turnOnDiscord(t, m)
	if err := m.SetWorldMods(m.Settings().World, []string{"advancedportals"}); err != nil {
		t.Fatal(err)
	}
	fake.HangCodes.Store(true)
	join := startOnline(t, m)
	m.Close() // the server keeps running; the code isn't made yet
	posted := chat.count("is online")

	fake.HangCodes.Store(false)
	m2 := newTestManager(t, env.data, env)
	m2.opt.ModsURL, m2.opt.DiscordURL = store.URL, chat.URL
	m2.Start()
	env.m = m2
	if posted != 0 {
		t.Errorf("posted %d times while waiting for the code", posted)
	}
	chat.wait(t, "Join code: **"+join+"**", 1)
	if n := chat.count("📦 Mods code: `"); n != 1 || m2.Status().ModsCode == "" {
		t.Fatalf("after taking over: %d posts with a mods code, status %q", n, m2.Status().ModsCode)
	}
}

package minecraft

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"valheim-easy-server/internal/game"
)

func sum1(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }

// mojang is a stand-in for Mojang's sites, answering from the recorded
// files in testdata (piston-meta's version list and 26.3's file, trimmed
// to what the app reads, and the profile API's answers). The links inside
// them are pointed at the stand-in, and their checksums at its files.
type mojang struct {
	srv      *httptest.Server
	jar      []byte
	jarCalls atomic.Int32
	manifest []byte
	version  []byte
}

func newMojang(t *testing.T, change func(manifest, version map[string]any)) *mojang {
	t.Helper()
	m := &mojang{jar: []byte("a stand-in server.jar")}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mc/game/version_manifest_v2.json":
			w.Write(m.manifest)
		case r.URL.Path == "/v1/packages/26.3.json":
			w.Write(m.version)
		case r.URL.Path == "/server.jar":
			m.jarCalls.Add(1)
			w.Write(m.jar)
		case strings.HasPrefix(r.URL.Path, "/profiles/"):
			if strings.EqualFold(strings.TrimPrefix(r.URL.Path, "/profiles/"), "Notch") {
				w.Write([]byte(readFile(t, filepath.Join("testdata", "mojang-profile-Notch.json"))))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(readFile(t, filepath.Join("testdata", "mojang-profile-404.json"))))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.srv.Close)

	var manifest, version map[string]any
	json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "piston-meta-manifest.json"))), &manifest)
	json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "piston-meta-26.3.json"))), &version)
	server := version["downloads"].(map[string]any)["server"].(map[string]any)
	server["url"], server["sha1"], server["size"] = m.srv.URL+"/server.jar", sum1(m.jar), len(m.jar)
	if change != nil {
		change(manifest, version)
	}
	m.version, _ = json.Marshal(version)
	for _, v := range manifest["versions"].([]any) {
		if v := v.(map[string]any); v["id"] == "26.3" {
			v["url"], v["sha1"] = m.srv.URL+"/v1/packages/26.3.json", sum1(m.version)
		}
	}
	m.manifest, _ = json.Marshal(manifest)

	oldMeta, oldProfiles := pistonMeta, mojangProfiles
	pistonMeta, mojangProfiles = m.srv.URL+"/mc/game/version_manifest_v2.json", m.srv.URL+"/profiles/"
	t.Cleanup(func() { pistonMeta, mojangProfiles = oldMeta, oldProfiles })
	return m
}

// TestRecordedAnswers pins the facts the recorded answers hold (FACTS.md):
// on 2026-11-26 the newest release is 26.3 (a snapshot, 26.4-pre-1, is
// newer and must be skipped), its server.jar has this SHA-1 and size, and
// it needs Java 25.
func TestRecordedAnswers(t *testing.T) {
	var m manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "piston-meta-manifest.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if m.Latest.Release != "26.3" || m.Versions[0].ID != "26.4-pre-1" || m.Versions[0].Type != "snapshot" {
		t.Errorf("recorded list: latest %q, first %q (%s)", m.Latest.Release, m.Versions[0].ID, m.Versions[0].Type)
	}
	var v versionJSON
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "piston-meta-26.3.json"))), &v); err != nil {
		t.Fatal(err)
	}
	srv := v.Downloads.Server
	if srv.SHA1 != "33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c" || srv.Size != 62294556 || v.JavaVersion.MajorVersion != 25 ||
		srv.URL != "https://piston-data.mojang.com/v1/objects/33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c/server.jar" {
		t.Errorf("recorded 26.3: %+v java %d", srv, v.JavaVersion.MajorVersion)
	}
}

func TestLatestRelease(t *testing.T) {
	m := newMojang(t, nil)
	rel, err := latestRelease(context.Background(), m.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "26.3" || rel.Java != 25 || rel.JarSHA1 != sum1(m.jar) || rel.Released.IsZero() {
		t.Errorf("release = %+v", rel)
	}
}

// TestLatestReleaseProblems: what the resolver refuses.
func TestLatestReleaseProblems(t *testing.T) {
	for name, c := range map[string]struct {
		change func(manifest, version map[string]any)
		want   string
	}{
		"a snapshot named as the release": {func(m, v map[string]any) { m["latest"].(map[string]any)["release"] = "26.4-pre-1" }, "snapshot"},
		"no server":                       {func(m, v map[string]any) { delete(v["downloads"].(map[string]any), "server") }, "no server"},
		"no checksum":                     {func(m, v map[string]any) { v["downloads"].(map[string]any)["server"].(map[string]any)["sha1"] = "" }, "checksum"},
		"no Java":                         {func(m, v map[string]any) { delete(v, "javaVersion") }, "Java"},
		"not listed":                      {func(m, v map[string]any) { m["latest"].(map[string]any)["release"] = "99.1" }, "doesn't list"},
	} {
		m := newMojang(t, c.change)
		if _, err := latestRelease(context.Background(), m.srv.Client()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The version file must match the list's checksum.
	m := newMojang(t, nil)
	m.version = append(m.version, ' ')
	if _, err := latestRelease(context.Background(), m.srv.Client()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a changed version file: %v", err)
	}
}

// TestInstallJar: the jar is downloaded and checked, a second install with
// the right jar downloads nothing, and older versions' unpacked folders go.
func TestInstallJar(t *testing.T) {
	m := newMojang(t, nil)
	s := Game{}.New(testEnv(t)).(*server)
	s.env.HTTP = m.srv.Client()
	rel, err := latestRelease(context.Background(), s.env.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(s.env.Dirs.Server, "versions", "26.2")
	os.MkdirAll(old, 0o755)
	var steps []string
	if err := s.installJar(context.Background(), rel, false, func(p game.Progress) { steps = append(steps, p.Step) }); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, s.jarPath()); got != string(m.jar) {
		t.Errorf("jar = %q", got)
	}
	if len(steps) == 0 || steps[0] != "Downloading the Minecraft 26.3 server…" {
		t.Errorf("progress %q", steps)
	}
	if fileExists(old) {
		t.Errorf("the old version's folder is still there")
	}
	if err := s.installJar(context.Background(), rel, false, func(game.Progress) {}); err != nil || m.jarCalls.Load() != 1 {
		t.Errorf("second install: %v, %d downloads", err, m.jarCalls.Load())
	}
	// A wrong jar on the site is refused and leaves the good one.
	m.jar = []byte("something else")
	if err := s.installJar(context.Background(), rel, true, func(game.Progress) {}); err == nil {
		t.Errorf("a jar with the wrong checksum was taken")
	}
	if got := readFile(t, s.jarPath()); got != "a stand-in server.jar" {
		t.Errorf("after a failed download the jar is %q", got)
	}
}

// TestMojangLookup: the profile API's id has no dashes; the list files need
// them. A name nobody has is refused.
func TestMojangLookup(t *testing.T) {
	m := newMojang(t, nil)
	p, err := mojangLookup(context.Background(), m.srv.Client(), "notch")
	if err != nil || p.uuid != "069a79f4-44e9-4726-a5be-fca90e38aaf5" || p.name != "Notch" {
		t.Errorf("Notch = %+v, %v", p, err)
	}
	if _, err := mojangLookup(context.Background(), m.srv.Client(), "zz_no_such_player_zz"); err == nil || !strings.Contains(err.Error(), "doesn't know") {
		t.Errorf("unknown name: %v", err)
	}
}

// TestListsWhileStopped: the files are written as Minecraft writes them,
// with account ids from the server's own cache or from Mojang, and fields
// the app doesn't know are kept.
func TestListsWhileStopped(t *testing.T) {
	m := newMojang(t, nil)
	s := Game{}.New(testEnv(t)).(*server)
	s.env.HTTP = m.srv.Client()
	os.MkdirAll(s.env.Dirs.Saves, 0o755)
	os.MkdirAll(s.env.Dirs.Server, 0o755)
	os.WriteFile(filepath.Join(s.env.Dirs.Saves, "usercache.json"), []byte(`[{"name":"Alex","uuid":"36532b5e-c442-3dbb-a24c-c7e55d0f979a","expiresOn":"2026-12-26 19:40:16 +0000"}]`), 0o644)
	os.WriteFile(s.listPath("ops.json"), []byte(`[{"uuid":"8667ba71-b85a-4004-af54-457a9734eed7","name":"Steve","level":2,"bypassesPlayerLimit":true}]`), 0o644)

	if err := s.WriteList(context.Background(), nil, "allowed", []string{"alex", "Notch", "ALEX"}); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	json.Unmarshal([]byte(readFile(t, s.listPath("whitelist.json"))), &got)
	if len(got) != 2 || got[0]["name"] != "Alex" || got[0]["uuid"] != "36532b5e-c442-3dbb-a24c-c7e55d0f979a" || got[1]["uuid"] != "069a79f4-44e9-4726-a5be-fca90e38aaf5" {
		t.Errorf("whitelist.json = %v", got)
	}
	if err := s.WriteList(context.Background(), nil, "admins", []string{"Steve", "Alex"}); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(readFile(t, s.listPath("ops.json"))), &got)
	if len(got) != 2 || got[0]["level"] != 2.0 || got[0]["bypassesPlayerLimit"] != true || got[1]["level"] != 4.0 {
		t.Errorf("ops.json = %v", got)
	}
	if err := s.WriteList(context.Background(), nil, "banned", []string{"Notch"}); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(readFile(t, s.listPath("banned-players.json"))), &got)
	if len(got) != 1 || got[0]["expires"] != "forever" || got[0]["created"] == "" {
		t.Errorf("banned-players.json = %v", got)
	}
	if err := s.WriteList(context.Background(), nil, "allowed", []string{"zz_no_such_player_zz"}); err == nil {
		t.Errorf("an unknown name was put on the list")
	}
	if err := s.WriteList(context.Background(), nil, "allowed", []string{"bad name!"}); err == nil {
		t.Errorf("a name with a space was taken")
	}
	if names, _ := s.ReadList("allowed"); len(names) != 2 {
		t.Errorf("a refused change changed the list: %q", names)
	}
	if err := s.WriteList(context.Background(), nil, "allowed", nil); err != nil {
		t.Fatal(err)
	}
	if b := readFile(t, s.listPath("whitelist.json")); strings.TrimSpace(b) != "[]" {
		t.Errorf("an emptied list is %q, want []", b)
	}
}

func TestDashUUID(t *testing.T) {
	for in, want := range map[string]string{
		"069a79f444e94726a5befca90e38aaf5":     "069a79f4-44e9-4726-a5be-fca90e38aaf5",
		"069A79F4-44E9-4726-A5BE-FCA90E38AAF5": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
		"069a79f444e9":                         "",
	} {
		if got, _ := dashUUID(in); got != want {
			t.Errorf("dashUUID(%q) = %q, want %q", in, got, want)
		}
	}
	for ip, want := range map[string]bool{
		"69.92.205.184": true, "203.0.113.7": true, "127.0.0.1": false, "192.168.1.20": false, "10.0.0.2": false,
		"100.64.1.2": false, "[2001:db8::1]": true, "[fd00::1]": false, "[0:0:0:0:0:0:0:1]": false, "**.**.**.**": false, "local": false,
	} {
		if got := publicAddr(ip); got != want {
			t.Errorf("publicAddr(%q) = %v", ip, got)
		}
	}
}

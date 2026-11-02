package mods

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"valheim-easy-server/internal/mods/modstest"
)

// unpackProfile reads export.r2x back out of a profile, as a mod manager does.
func unpackProfile(t *testing.T, profile string) string {
	t.Helper()
	body, ok := strings.CutPrefix(profile, "#r2modman\n")
	if !ok {
		t.Fatalf("no #r2modman line: %.40q", profile)
	}
	b, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "export.r2x" {
		t.Fatalf("files in the profile: %v", zr.File)
	}
	f, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	y, _ := io.ReadAll(f)
	return string(y)
}

// The list is laid out like the one r2modman exports.
func TestProfileFormat(t *testing.T) {
	profile, err := Profile("Valheim Midgard", []Package{{Loader, "5.4.2360"}, {"Gurebu-Riverheim", "1.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `profileName: "Valheim Midgard"
mods:
  - name: denikson-BepInExPack_Valheim
    version:
      major: 5
      minor: 4
      patch: 2360
    enabled: true
  - name: Gurebu-Riverheim
    version:
      major: 1
      minor: 2
      patch: 1
    enabled: true
`
	if got := unpackProfile(t, profile); got != want {
		t.Fatalf("export.r2x:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []Package{{"Gurebu-Riverheim", "1.2"}, {"Gurebu-Riverheim", "1.x.3"}, {"../evil", "1.0.0"}} {
		if _, err := Profile("x", []Package{bad}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestProfileName(t *testing.T) {
	for in, want := range map[string]string{
		"Midgard":                  "Valheim Midgard",
		"  Sky/Island: 2! ":        "Valheim Sky Island 2",
		"Björn's_World-3":          "Valheim Bjorn s_World-3",
		"Ragnarök Æsir Þing":       "Valheim Ragnarok Aesir Thing",
		"Mars.2":                   "Valheim Mars.2",
		"🐺":                        "Valheim",
		strings.Repeat("Long", 20): "Valheim " + strings.Repeat("Long", 10),
	} {
		if got := ProfileName(in); got != want {
			t.Errorf("ProfileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Players get the server's own versions of the mods it runs, and the latest
// of the ones just for their own game, with everything each one needs.
func TestProfilePackages(t *testing.T) {
	in, fake, _ := setup(t)
	ctx := context.Background()
	ids := []string{"advancedportals", "farlands"}
	if _, err := in.ProfilePackages(ctx, []string{"advancedportals"}); err == nil {
		t.Fatal("a profile for mods the server hasn't installed")
	}
	if _, err := in.Prepare(ctx, ids, false, nil); err != nil {
		t.Fatal(err)
	}
	fake.SetVersion(modstest.Portal, "1.3.0") // newer than the server's: players still get the server's
	fake.SetVersion(modstest.Loader, "5.4.2400")
	got, err := in.ProfilePackages(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	want := []Package{{Loader, "5.4.2351"}, {modstest.FarLands, "1.0.6"}, {modstest.Portal, "1.2.0"}, {modstest.Jotunn, "2.30.2"}}
	if len(got) != len(want) {
		t.Fatalf("packages %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("packages %v, want %v", got, want)
		}
	}
	// A world whose mods are all just for players' own games: the latest.
	only, err := in.ProfilePackages(ctx, []string{"farlands"})
	if err != nil || len(only) != 2 || only[0] != (Package{Loader, "5.4.2400"}) || only[1] != (Package{modstest.FarLands, "1.0.6"}) {
		t.Fatalf("players' mods only: %v %v", only, err)
	}
	if none, err := in.ProfilePackages(ctx, nil); none != nil || err != nil {
		t.Fatalf("a world without mods: %v %v", none, err)
	}
}

func TestShareCode(t *testing.T) {
	in, fake, _ := setup(t)
	ctx := context.Background()
	profile, err := Profile("Valheim Midgard", []Package{{Loader, "5.4.2351"}})
	if err != nil {
		t.Fatal(err)
	}
	code, err := in.ShareCode(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if fake.Profiles()[code] != profile {
		t.Fatalf("code %q, uploaded %v", code, fake.Profiles())
	}
	if ok, err := in.CodeWorks(ctx, code); !ok || err != nil {
		t.Fatalf("a new code doesn't work: %v %v", ok, err)
	}
	fake.Expire(code)
	if ok, err := in.CodeWorks(ctx, code); ok || err != nil {
		t.Fatalf("an expired code works: %v %v", ok, err)
	}
	if ok, _ := in.CodeWorks(ctx, "../../package"); ok {
		t.Fatal("a made-up code works")
	}
}

//go:build realthunderstore

package mods

// The real Thunderstore: a profile goes up, its code works, and what comes
// back through the code is what went up, as a mod manager fetches it. Run it
// with: go test -tags realthunderstore -run TestRealShareCode -v ./internal/mods/

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRealShareCode(t *testing.T) {
	in := &Installer{}
	ctx := context.Background()
	profile, err := Profile(ProfileName("Midgard"), []Package{{Loader, "5.4.2360"}, {"Advize-PlantEverything", "1.21.4"}})
	if err != nil {
		t.Fatal(err)
	}
	code, err := in.ShareCode(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("code %s", code)
	if ok, err := in.CodeWorks(ctx, code); !ok || err != nil {
		t.Fatalf("the new code doesn't work: %v %v", ok, err)
	}
	if ok, err := in.CodeWorks(ctx, "00000000-0000-4000-8000-000000000000"); ok || err != nil {
		t.Fatalf("a code that was never made works: %v %v", ok, err)
	}
	req, _ := http.NewRequest(http.MethodGet, DefaultURL+"/api/experimental/legacyprofile/get/"+code+"/", nil)
	req.Header.Set("User-Agent", "ValheimEasyServer")
	res, err := http.DefaultClient.Do(req) // follows the redirect to the file, like r2modman
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != profile {
		t.Fatalf("got back %s: %.200q", res.Status, b)
	}
	t.Logf("export.r2x:\n%s", unpackProfile(t, string(b)))
}

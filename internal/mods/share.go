package mods

// Share codes: a mod manager profile (r2modman, Thunderstore Mod Manager,
// Gale) kept on Thunderstore, which friends import with a code to get
// exactly the mods a world needs, at the server's versions, in one step.
// It's the same thing those mod managers make with "Export profile as code".

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Package is one Thunderstore package at one version.
type Package struct {
	Name    string `json:"name"`    // Namespace-Name
	Version string `json:"version"` // like 1.2.3
}

// ProfilePackages lists what players install for a world with the mods in
// ids: the mod loader, each mod and everything it needs. Mods the server
// runs come at the versions it has installed (Prepare must have run first);
// mods just for players' own games, which the server doesn't install, at
// Thunderstore's latest. It returns nothing for a world without mods.
func (in *Installer) ProfilePackages(ctx context.Context, ids []string) ([]Package, error) {
	rec := in.read()
	want := map[string]string{} // package name -> version
	var server, players []string
	for _, id := range ids {
		m, ok := Find(id)
		if !ok {
			return nil, fmt.Errorf("unknown mod %q", id)
		}
		if m.Players == PlayersOnly {
			players = append(players, m.Package)
		} else {
			server = append(server, m.Package)
		}
	}
	if len(server)+len(players) == 0 {
		return nil, nil
	}
	if len(server) > 0 {
		for name := range rec.closure(append([]string{Loader}, server...)) {
			p, ok := rec.Packages[name]
			if !ok || p.Version == "" {
				return nil, fmt.Errorf("%s isn't installed on the server", Title(name))
			}
			want[name] = p.Version
		}
	}
	if len(players) > 0 {
		pkgs, err := in.resolve(ctx, append([]string{Loader}, players...))
		if err != nil {
			return nil, err
		}
		for _, p := range pkgs {
			if _, ok := want[p.Name]; ok {
				continue // the server's version, so everyone has the same
			}
			if cur, ok := rec.Packages[p.Name]; ok && cur.Version != "" {
				want[p.Name] = cur.Version
			} else {
				want[p.Name] = p.Version
			}
		}
	}
	out := make([]Package, 0, len(want))
	for name, v := range want {
		out = append(out, Package{Name: name, Version: v})
	}
	slices.SortFunc(out, func(a, b Package) int { // the loader first, then by name
		switch {
		case a.Name == Loader:
			return -1
		case b.Name == Loader:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// ProfileName makes a name for a mod manager profile from a world's name:
// letters, digits, spaces, dashes and underscores, as profile folders allow.
func ProfileName(world string) string {
	var b strings.Builder
	for _, r := range world {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(name); len(r) > 40 {
		name = strings.TrimSpace(string(r[:40]))
	}
	if name == "" {
		return "Valheim"
	}
	return "Valheim " + name
}

// Profile builds what a mod manager imports for these packages: the line
// "#r2modman", then a base64 zip holding export.r2x, the list of mods.
func Profile(name string, pkgs []Package) (string, error) {
	var y strings.Builder
	fmt.Fprintf(&y, "profileName: %s\nmods:\n", strconv.Quote(name)) // a YAML double-quoted string
	for _, p := range pkgs {
		if !safeName.MatchString(p.Name) {
			return "", fmt.Errorf("unexpected package name %q", p.Name)
		}
		v := strings.Split(p.Version, ".")
		if len(v) != 3 {
			return "", fmt.Errorf("unexpected version %q of %s", p.Version, p.Name)
		}
		var n [3]int
		for i, s := range v {
			x, err := strconv.Atoi(s)
			if err != nil || x < 0 {
				return "", fmt.Errorf("unexpected version %q of %s", p.Version, p.Name)
			}
			n[i] = x
		}
		fmt.Fprintf(&y, "  - name: %s\n    version:\n      major: %d\n      minor: %d\n      patch: %d\n    enabled: true\n", p.Name, n[0], n[1], n[2])
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "export.r2x", Method: zip.Deflate, Modified: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, y.String()); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return "#r2modman\n" + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

var codeRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ShareCode puts a profile on Thunderstore and returns the code that
// imports it.
func (in *Installer) ShareCode(ctx context.Context, profile string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.base()+"/api/experimental/legacyprofile/create/", strings.NewReader(profile))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "ValheimEasyServer")
	res, err := in.client().Do(req)
	if err != nil {
		return "", errors.New("couldn't reach Thunderstore. Is the internet connection working?")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return "", errors.New("Thunderstore is busy; it makes the code at the next start")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("Thunderstore answered %s", res.Status)
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body); err != nil || !codeRE.MatchString(body.Key) {
		return "", errors.New("Thunderstore didn't give a code")
	}
	return strings.ToLower(body.Key), nil
}

// CodeWorks reports whether Thunderstore still has the profile with this
// code: codes stop working after a while.
func (in *Installer) CodeWorks(ctx context.Context, code string) (bool, error) {
	if !codeRE.MatchString(code) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base()+"/api/experimental/legacyprofile/get/"+code+"/", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	c := *in.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse } // the redirect says it's there
	res, err := c.Do(req)
	if err != nil {
		return false, errors.New("couldn't reach Thunderstore")
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return false, nil
	case res.StatusCode >= 200 && res.StatusCode < 400:
		return true, nil
	}
	return false, fmt.Errorf("Thunderstore answered %s", res.Status)
}

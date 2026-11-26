package minecraft

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// pistonMeta is Mojang's version list. Tests point it at recorded answers.
var pistonMeta = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

// release is one Minecraft release as the app installs it.
type release struct {
	Version  string    // "26.3": an opaque string, never parsed as numbers
	Released time.Time // when Mojang published it
	JarURL   string
	JarSHA1  string
	JarSize  int64
	Java     int // the Java major version it needs (25 for 26.3)
}

// manifest is the part of version_manifest_v2.json the app reads.
type manifest struct {
	Latest struct {
		Release string `json:"release"`
	} `json:"latest"`
	Versions []struct {
		ID          string    `json:"id"`
		Type        string    `json:"type"`
		URL         string    `json:"url"`
		SHA1        string    `json:"sha1"`
		ReleaseTime time.Time `json:"releaseTime"`
	} `json:"versions"`
}

// versionJSON is the part of one version's JSON the app reads.
type versionJSON struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Downloads struct {
		Server struct {
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
	JavaVersion struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
}

var reSHA1 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// latestRelease asks Mojang for the newest release (never a snapshot) and
// where its server.jar is.
//
// The version list gives each version's JSON with a SHA-1, which is checked
// too, so the jar's SHA-1 that the download is checked against comes from a
// file whose own checksum came from the list.
func latestRelease(ctx context.Context, client *http.Client) (release, error) {
	var m manifest
	if _, err := getJSON(ctx, client, pistonMeta, "", &m); err != nil {
		return release{}, err
	}
	want := m.Latest.Release
	if want == "" {
		return release{}, errors.New("Mojang's version list doesn't name a current release.")
	}
	for _, v := range m.Versions {
		if v.ID != want {
			continue
		}
		if v.Type != "release" {
			return release{}, fmt.Errorf("Mojang's version list calls %s the newest release, but lists it as a %q.", want, v.Type)
		}
		var vj versionJSON
		if _, err := getJSON(ctx, client, v.URL, v.SHA1, &vj); err != nil {
			return release{}, err
		}
		srv := vj.Downloads.Server
		switch {
		case vj.ID != want:
			return release{}, fmt.Errorf("Mojang's file for Minecraft %s describes %q instead.", want, vj.ID)
		case srv.URL == "":
			return release{}, fmt.Errorf("Mojang publishes no server for Minecraft %s.", want)
		case !reSHA1.MatchString(srv.SHA1):
			return release{}, fmt.Errorf("Mojang didn't publish a usable checksum for the Minecraft %s server, so the app won't install it.", want)
		case vj.JavaVersion.MajorVersion <= 0:
			return release{}, fmt.Errorf("Mojang's file for Minecraft %s doesn't say which Java it needs.", want)
		}
		return release{Version: want, Released: v.ReleaseTime, JarURL: srv.URL, JarSHA1: srv.SHA1, JarSize: srv.Size, Java: vj.JavaVersion.MajorVersion}, nil
	}
	return release{}, fmt.Errorf("Mojang's version list names %s as the newest release but doesn't list it.", want)
}

// getJSON reads a small JSON answer from Mojang. With sha1Hex set, the
// answer must have that SHA-1.
func getJSON(ctx context.Context, client *http.Client, link, sha1Hex string, v any) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// A list is small: a site that hasn't answered in a minute won't.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Couldn't reach Mojang's download site. Check the internet connection. (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mojang's download site answered %s.", resp.Status)
	}
	// The version list is about 300 kB, a version's file about 50 kB.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("Couldn't read Mojang's answer (%v).", err)
	}
	if sha1Hex != "" {
		sum := sha1.Sum(b)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), sha1Hex) {
			return nil, errors.New("Mojang's answer didn't match its published checksum; try again later.")
		}
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("Mojang's download site answered something the app doesn't understand (%v).", err)
	}
	return b, nil
}

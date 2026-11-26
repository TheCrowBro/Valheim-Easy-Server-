package minecraft

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
	"valheim-easy-server/internal/platform"
)

// State keys this adapter keeps in Saved.State (servers.json).
const (
	// stateEULA is when the owner ticked "I agree to the Minecraft EULA"
	// (RFC 3339). Without it the server can't start (spec 5.2).
	stateEULA = "eula"
	// stateJava is the Java major version installed for the server
	// ("25"), so an update only fetches a new Java when Minecraft needs
	// a different one.
	stateJava = "java"
	// stateUnsupported is set, with the reason, when the Java the newest
	// Minecraft needs can't start on this Mac (the owner's answer to Q6:
	// Minecraft then shows as "Needs Windows" there).
	stateUnsupported = "unsupported"
)

// macTooOld is the owner's decided wording (spec 10.2 Q6).
const macTooOld = "Needs Windows: the newest Minecraft needs a newer macOS"

// Installer installs the server: the stand-in in fake mode, else Mojang's
// newest release and the Java it needs.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		// The stand-in goes where this instance's Java would be, and an
		// empty server.jar where the plan expects the real one.
		return install.Fake(s.env.Fake, s.Program(), []string{s.jarPath()})
	}
	return realInstaller{s}
}

type realInstaller struct{ s *server }

func (r realInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if progress == nil {
		progress = func(game.Progress) {}
	}
	return r.s.install(ctx, repair, progress)
}

// install puts the newest release and its Java in place. A repair first
// removes the Java and what the jar unpacked, so both come back fresh; the
// worlds, the lists and server.properties are never touched.
func (s *server) install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	progress(game.Progress{Step: "Asking Mojang for the newest Minecraft…", Percent: -1})
	rel, err := latestRelease(ctx, s.env.HTTP)
	if err != nil {
		return err
	}
	if repair {
		for _, d := range []string{s.env.Dirs.Runtime, filepath.Join(s.env.Dirs.Server, "versions"), filepath.Join(s.env.Dirs.Server, "libraries")} {
			if err := os.RemoveAll(d); err != nil {
				return fmt.Errorf("Couldn't remove the old files in %s (%v). Is the server still running?", d, err)
			}
		}
	}
	return s.installRelease(ctx, rel, repair, true, progress)
}

// installRelease installs one release. Java goes first, so a Java that
// can't be installed (or can't start on this Mac) leaves the old jar, which
// still runs on the old Java. withJava false keeps the Java that's there,
// when the release needs the same major version.
func (s *server) installRelease(ctx context.Context, rel release, repair, withJava bool, progress func(game.Progress)) error {
	if withJava {
		if err := os.MkdirAll(s.env.Dirs.Runtime, 0o755); err != nil {
			return err
		}
		java, err := install.Temurin(ctx, rel.Java, s.env.Dirs.Runtime, progress)
		if err != nil {
			return err
		}
		progress(game.Progress{Step: "Checking that Java starts…", Percent: -1})
		if err := javaStarts(ctx, java); err != nil {
			if runtime.GOOS == "darwin" {
				s.saveState(stateUnsupported, macTooOld)
				return fmt.Errorf("%s (Java %d doesn't start on this Mac: %v).", macTooOld, rel.Java, err)
			}
			return fmt.Errorf("The Java %d the app installed for Minecraft doesn't start (%v). Try Repair.", rel.Java, err)
		}
		s.saveState(stateJava, strconv.Itoa(rel.Java))
		s.saveState(stateUnsupported, "")
	}
	if err := s.installJar(ctx, rel, repair, progress); err != nil {
		return err
	}
	return install.WriteBuild(s.env.Dirs.Server, game.Build{Version: rel.Version, Date: rel.Released.UTC()})
}

// installJar downloads the release's server.jar, checked against Mojang's
// SHA-1, unless the right one is already there.
func (s *server) installJar(ctx context.Context, rel release, repair bool, progress func(game.Progress)) error {
	jar := s.jarPath()
	if !repair && fileSHA1(jar) == strings.ToLower(rel.JarSHA1) {
		return nil
	}
	if err := os.MkdirAll(s.env.Dirs.Server, 0o755); err != nil {
		return err
	}
	text := "Downloading the Minecraft " + rel.Version + " server…"
	err := install.Download(ctx, rel.JarURL, install.Hash{Algo: "sha1", Hex: rel.JarSHA1}, jar, func(p game.Progress) {
		p.Step = text
		progress(p)
	})
	if err != nil {
		return err
	}
	// The jar unpacks itself into versions/<version>/ at its first start;
	// older versions' folders are no use any more.
	if entries, err := os.ReadDir(filepath.Join(s.env.Dirs.Server, "versions")); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != rel.Version {
				os.RemoveAll(filepath.Join(s.env.Dirs.Server, "versions", e.Name()))
			}
		}
	}
	return nil
}

// update is the "update on start": when Mojang has a newer release than
// the one installed, install it before the server starts. Only a change of
// Minecraft version can need a new Java, so Adoptium is asked only then.
func (s *server) update(ctx context.Context, step func(string, float64)) error {
	installed, _ := install.ReadBuild(s.env.Dirs.Server)
	step("Checking for Minecraft updates…", -1)
	rel, err := latestRelease(ctx, s.env.HTTP)
	if err != nil {
		return err
	}
	if rel.Version == installed.Version && isFile(s.jarPath()) {
		return nil
	}
	saved := s.store.Get()
	needJava := saved.State[stateJava] != strconv.Itoa(rel.Java) || !isFile(s.Program())
	return s.installRelease(ctx, rel, false, needJava, func(p game.Progress) { step(p.Step, p.Percent) })
}

// javaStarts runs "java -version", the cheapest proof that this Java runs
// on this computer (a Java built for a newer macOS stops at once).
func javaStarts(ctx context.Context, java string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, java, "-version")
	cmd.SysProcAttr = platform.HiddenProcAttr() // no console window on Windows
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n"); line != "" {
			return fmt.Errorf("%v: %s", err, line)
		}
		return err
	}
	return nil
}

// saveState records one of the adapter's own values ("" removes it). The
// installer and the plan run outside the manager's lock, so saving here
// is allowed (spec 2.2, Locking).
func (s *server) saveState(key, value string) {
	err := s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		if value == "" {
			delete(sv.State, key)
		} else {
			sv.State[key] = value
		}
		return nil
	})
	if err != nil && s.env.Log != nil {
		s.env.Log.Printf("minecraft: couldn't save %s: %v", key, err)
	}
}

// fileSHA1 is the file's SHA-1 in lower-case hex, "" when it can't be read.
func fileSHA1(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// errNotInstalled is what a start says when the files are missing.
var errNotInstalled = errors.New("The Minecraft server isn't fully installed. Click Repair to install it again.")

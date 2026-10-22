package steamcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Fake pretends to install the server by copying a stand-in program (see
// cmd/fakevalheim). It is used for development and tests only.
type Fake struct {
	Source     string        // the stand-in server program
	InstallDir string        // where to put it
	ExeName    string        // file name the app expects
	Duration   time.Duration // how long the pretend download takes
}

// SetInstallDir changes where the server is installed.
func (f *Fake) SetInstallDir(dir string) { f.InstallDir = dir }

// Install implements Installer.
func (f *Fake) Install(ctx context.Context, validate bool, progress func(Progress)) error {
	steps := []string{"Updating SteamCMD…", "Downloading the Valheim server…", "Checking the server files…"}
	if _, err := os.Stat(filepath.Join(f.InstallDir, f.ExeName)); err == nil && !validate {
		steps = []string{"Checking the server files…"}
	}
	per := f.Duration / time.Duration(len(steps)*10)
	for _, step := range steps {
		for pct := 0; pct <= 100; pct += 10 {
			progress(Progress{Step: step, Percent: float64(pct)})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(per):
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(f.InstallDir, "steamapps"), 0o755); err != nil {
		return err
	}
	in, err := os.Open(f.Source)
	if err != nil {
		return fmt.Errorf("fake server program missing: %w", err)
	}
	defer in.Close()
	dst := filepath.Join(f.InstallDir, f.ExeName)
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	manifest := fmt.Sprintf("\"AppState\"\n{\n\t\"appid\"\t\t\"%s\"\n\t\"buildid\"\t\t\"20412345\"\n\t\"LastUpdated\"\t\t\"%d\"\n}\n", AppID, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(f.InstallDir, "steamapps", "appmanifest_"+AppID+".acf"), []byte(manifest), 0o644); err != nil {
		return err
	}
	progress(Progress{Step: "Server files are up to date.", Percent: 100})
	return nil
}

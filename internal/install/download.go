package install

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// Hash is a published checksum. Sources publish different ones: Mojang sha1, Adoptium
// sha256, Vintage Story md5, .NET sha512.
type Hash struct {
	Algo string // "md5", "sha1", "sha256", "sha512"; "" = none
	Hex  string
}

// hasher returns a fresh hash for h and the sum it must come to; both nil
// when h asks for no check.
func (h Hash) hasher() (hash.Hash, []byte, error) {
	var x hash.Hash
	switch strings.ToLower(h.Algo) {
	case "":
		return nil, nil, nil
	case "md5":
		x = md5.New()
	case "sha1":
		x = sha1.New()
	case "sha256":
		x = sha256.New()
	case "sha512":
		x = sha512.New()
	default:
		return nil, nil, fmt.Errorf("the download's checksum type %q isn't one the app knows", h.Algo)
	}
	want, err := hex.DecodeString(strings.TrimSpace(h.Hex))
	if err != nil || len(want) != x.Size() {
		return nil, nil, fmt.Errorf("the download's published %s checksum (%q) isn't valid", h.Algo, h.Hex)
	}
	return x, want, nil
}

// errChecksum is inside the error a download whose checksum doesn't match
// returns.
var errChecksum = errors.New("checksum mismatch")

// stallTimeout is how long a download may go without receiving anything
// before it is given up. Big downloads have no overall time limit, so a
// connection that silently died must not hang an install for ever.
var stallTimeout = 2 * time.Minute

var errStalled = errors.New("the download stopped")

// Download fetches url into a temp file next to dest, checks the hash, then renames.
// Unzip/Untar refuse unsafe paths, keep or set exec bits.
//
// dest is only ever the whole, checked file: a failed or cancelled download
// leaves whatever was there before. step gets "Downloading <file>…" with the
// percentage when the size is known (-1 when not); callers that want their
// own words can rewrite Step.
func Download(ctx context.Context, url string, hash Hash, dest string, step func(game.Progress)) error {
	if step == nil {
		step = func(game.Progress) {}
	}
	sum, want, err := hash.hasher()
	if err != nil {
		return err
	}
	name := filepath.Base(dest)
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	// The watchdog runs from the request on: no answer, or no data, for
	// stallTimeout cancels the download.
	watchdog := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	defer watchdog.Stop()
	resp, err := client.Do(req)
	if err != nil {
		return downloadError(ctx, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Couldn't download %s (the server said %s).", name, resp.Status)
	}

	tmp, err := os.CreateTemp(dir, "."+name+".*.part")
	if err != nil {
		return err
	}
	kept := false
	defer func() {
		if !kept {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	text := "Downloading " + name + "…"
	body := &progressReader{r: resp.Body, total: resp.ContentLength, report: func(pct float64) {
		step(game.Progress{Step: text, Percent: pct})
	}, moved: func() { watchdog.Reset(stallTimeout) }}
	if resp.ContentLength > 0 {
		step(game.Progress{Step: text, Percent: 0})
	} else {
		step(game.Progress{Step: text, Percent: -1})
	}
	var w io.Writer = tmp
	if sum != nil {
		w = io.MultiWriter(tmp, sum)
	}
	if _, err := io.Copy(w, body); err != nil {
		return downloadError(ctx, name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if sum != nil && !bytes.Equal(sum.Sum(nil), want) {
		return fmt.Errorf("The download of %s was damaged: its %s checksum doesn't match the published one. Try again. (%w)",
			name, strings.ToLower(hash.Algo), errChecksum)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	kept = true
	return nil
}

// downloadError says in plain words why a download failed, keeping a
// cancel recognisable to the caller.
func downloadError(ctx context.Context, name string, err error) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errStalled):
		return fmt.Errorf("The download of %s stopped making progress. Check the internet connection and try again.", name)
	case cause != nil:
		return cause
	}
	return fmt.Errorf("Couldn't download %s. Check the internet connection. (%v)", name, err)
}

// progressReader reports whole-percent steps as a download goes, and tells
// the watchdog that data is still arriving.
type progressReader struct {
	r      io.Reader
	total  int64
	read   int64
	last   float64
	report func(float64)
	moved  func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.moved()
		p.read += int64(n)
		if p.total > 0 {
			pct := float64(p.read * 100 / p.total) // whole percents: no flood of steps
			if pct > p.last {
				p.last = pct
				p.report(pct)
			}
		}
	}
	return n, err
}

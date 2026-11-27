package satisfactory

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

// A .sav file starts with a header that names the session (the world) the
// save belongs to (FACTS.md: the game's own FSaveHeader, as the modding
// headers and the wiki describe it). The app reads only its start:
//
//	int32   header version (14 in 1.1 and later)
//	int32   save version
//	int32   build version (the game's build number)
//	FString save name          (header version 14 and later)
//	FString map name
//	FString map options
//	FString session name       (header version 4 and later; an int32 before)
//	int32   played seconds     (version 3 and later)
//	int64   saved at, in .NET ticks (version 4 and later)
//
// An FString is an int32 length counting the closing zero, then that many
// bytes (ASCII), or, when the length is negative, that many UTF-16 units.

// saveHeader is what the app reads of a .sav file's header.
type saveHeader struct {
	HeaderVersion int32
	SaveVersion   int32
	BuildVersion  int32
	SaveName      string
	MapName       string
	MapOptions    string
	SessionName   string
	PlayedSeconds int32
	SavedAt       time.Time
}

// Header versions the reader knows (from the game's FSaveHeader).
const (
	headerSessionString = 4  // the session name became a string, the save time was added
	headerSaveName      = 14 // the save's own name was added after the build
	headerNewest        = 64 // a sanity bound: far beyond any version seen
	maxFString          = 1 << 16
)

var errNotSave = errors.New("not a Satisfactory save")

// readSaveHeaderFile reads the header of the save at path.
func readSaveHeaderFile(path string) (saveHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return saveHeader{}, err
	}
	defer f.Close()
	return readSaveHeader(bufio.NewReaderSize(f, 4096))
}

// readSaveHeader reads a save's header from r.
func readSaveHeader(r io.Reader) (saveHeader, error) {
	var h saveHeader
	rd := &headerReader{r: r}
	h.HeaderVersion = rd.int32()
	if rd.err == nil && (h.HeaderVersion < headerSessionString || h.HeaderVersion > headerNewest) {
		return h, fmt.Errorf("%w (header version %d)", errNotSave, h.HeaderVersion)
	}
	h.SaveVersion = rd.int32()
	h.BuildVersion = rd.int32()
	if h.HeaderVersion >= headerSaveName {
		h.SaveName = rd.fstring()
	}
	h.MapName = rd.fstring()
	h.MapOptions = rd.fstring()
	h.SessionName = rd.fstring()
	h.PlayedSeconds = rd.int32()
	h.SavedAt = ticksTime(rd.int64())
	if rd.err != nil {
		return saveHeader{}, fmt.Errorf("%w: %v", errNotSave, rd.err)
	}
	return h, nil
}

// ticksTime turns .NET ticks (100 ns since the year 1, UTC) into a time.
func ticksTime(ticks int64) time.Time {
	if ticks <= 0 {
		return time.Time{}
	}
	const unixTicks = 621355968000000000 // ticks at 1970-01-01
	sec := (ticks - unixTicks) / 10_000_000
	nsec := (ticks - unixTicks) % 10_000_000 * 100
	return time.Unix(sec, nsec).UTC()
}

// timeTicks is the reverse, for tests.
func timeTicks(t time.Time) int64 {
	const unixTicks = 621355968000000000
	return unixTicks + t.Unix()*10_000_000 + int64(t.Nanosecond()/100)
}

// headerReader reads little-endian values, keeping the first error.
type headerReader struct {
	r   io.Reader
	err error
}

func (h *headerReader) read(n int) []byte {
	if h.err != nil {
		return nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(h.r, b); err != nil {
		h.err = err
		return nil
	}
	return b
}

func (h *headerReader) int32() int32 {
	b := h.read(4)
	if b == nil {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(b))
}

func (h *headerReader) int64() int64 {
	b := h.read(8)
	if b == nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(b))
}

func (h *headerReader) fstring() string {
	n := h.int32()
	switch {
	case h.err != nil || n == 0:
		return ""
	case n > maxFString || n < -maxFString:
		h.err = fmt.Errorf("a text of %d characters in the header", n)
		return ""
	case n > 0:
		b := h.read(int(n))
		return strings.TrimRight(string(b), "\x00")
	}
	b := h.read(int(-n) * 2)
	if b == nil {
		return ""
	}
	u := make([]uint16, -n)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

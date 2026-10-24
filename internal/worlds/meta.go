// Package worlds manages the Valheim worlds in the server's save folder:
// listing them, backing up and restoring, deleting, and moving worlds in and
// out as zip files.
//
// Valheim has two world formats:
//
//   - Classic (before 1.0): Name.fwl (name and seed) and Name.db (the world).
//   - 1.0 and later: a folder Name/ holding _main.<N>.fwl2, _main.<N>.db2,
//     _main.<N>.chunks, _main.<N>.ok and many *.chunk files. N goes up each
//     time the world saves.
//
// The 1.0 server converts a classic world into a folder the first time it
// loads it, keeping Name.fwl.old/Name.db.old and a Name_backup_<time> copy.
package worlds

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"unicode/utf8"
)

// Meta is what a world's metadata file (.fwl or .fwl2) says about it.
type Meta struct {
	Version  int
	Name     string
	SeedName string
	Seed     int32
	UID      int64
}

// maxMeta is far more than a world's metadata file ever holds.
const maxMeta = 1 << 20

// readMeta reads a world's small metadata file (.fwl or .fwl2), refusing
// one too big to be one rather than reading it into memory whole.
func readMeta(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMeta+1))
	if err == nil && len(data) > maxMeta {
		err = errors.New("the file is too big to be a Valheim world's .fwl")
	}
	return data, err
}

// ParseMeta reads a Valheim world metadata file. The file is a 4-byte
// length followed by a "package": world version (int32), name (string),
// seed name (string), seed (int32), unique ID (int64) and newer fields.
// Strings are .NET BinaryWriter strings: a 7-bit length, then UTF-8.
func ParseMeta(data []byte) (Meta, error) {
	var m Meta
	r := bytes.NewReader(data)
	var n int32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return m, err
	}
	if n <= 0 || int(n) > len(data)-4 {
		return m, errors.New("not a world metadata file")
	}
	pkg := bytes.NewReader(data[4 : 4+n])
	var ver int32
	if err := binary.Read(pkg, binary.LittleEndian, &ver); err != nil {
		return m, err
	}
	m.Version = int(ver)
	var err error
	if m.Name, err = readString(pkg); err != nil {
		return m, err
	}
	if m.SeedName, err = readString(pkg); err != nil {
		return m, err
	}
	if err := binary.Read(pkg, binary.LittleEndian, &m.Seed); err != nil {
		return m, err
	}
	if err := binary.Read(pkg, binary.LittleEndian, &m.UID); err != nil {
		return m, err
	}
	if m.Version < 1 || m.Version > 10000 || m.Name == "" {
		return m, errors.New("not a world metadata file")
	}
	return m, nil
}

func readString(r *bytes.Reader) (string, error) {
	n, err := read7BitInt(r)
	if err != nil {
		return "", err
	}
	if n < 0 || n > 1024 || n > r.Len() {
		return "", errors.New("bad string length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("bad string")
	}
	return string(b), nil
}

func read7BitInt(r *bytes.Reader) (int, error) {
	result, shift := 0, 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift > 28 {
			return 0, errors.New("bad 7-bit integer")
		}
	}
}

func write7BitInt(w *bytes.Buffer, n int) {
	for n >= 0x80 {
		w.WriteByte(byte(n) | 0x80)
		n >>= 7
	}
	w.WriteByte(byte(n))
}

func writeString(w *bytes.Buffer, s string) {
	write7BitInt(w, len(s))
	w.WriteString(s)
}

// RenameMeta returns a copy of a metadata file with the world name changed
// and everything else byte-for-byte the same.
func RenameMeta(data []byte, newName string) ([]byte, error) {
	if _, err := ParseMeta(data); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(data[:4]))
	pkg := data[4 : 4+n]
	r := bytes.NewReader(pkg[4:]) // after the version
	oldLen := r.Len()
	if _, err := readString(r); err != nil {
		return nil, err
	}
	nameEnd := 4 + (oldLen - r.Len())

	var body bytes.Buffer
	body.Write(pkg[:4])
	writeString(&body, newName)
	body.Write(pkg[nameEnd:])

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, int32(body.Len()))
	out.Write(body.Bytes())
	out.Write(data[4+n:]) // anything after the package is kept as is
	return out.Bytes(), nil
}

// BuildMeta creates a metadata file; used by tests and the simulator.
func BuildMeta(m Meta, needsDB bool) []byte {
	var body bytes.Buffer
	binary.Write(&body, binary.LittleEndian, int32(m.Version))
	writeString(&body, m.Name)
	writeString(&body, m.SeedName)
	binary.Write(&body, binary.LittleEndian, m.Seed)
	binary.Write(&body, binary.LittleEndian, m.UID)
	binary.Write(&body, binary.LittleEndian, int32(2)) // world generator version
	if needsDB {
		body.WriteByte(1)
	} else {
		body.WriteByte(0)
	}
	binary.Write(&body, binary.LittleEndian, int32(0)) // starting global keys
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, int32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

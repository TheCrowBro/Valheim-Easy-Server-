package satisfactory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
	"unicode/utf16"
)

// A 1.1 header (version 14) with the wiki's example values: the session
// name comes after the save's own name.
func TestSaveHeaderV14(t *testing.T) {
	at := time.Date(2025, 3, 1, 18, 30, 15, 123456700, time.UTC)
	b := append(buildHeader(14, "only one way to find out_autosave_0", "only one way to find out", 7200, at), 0xAA, 0xBB)
	h, err := readSaveHeader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if h.HeaderVersion != 14 || h.SaveVersion != 52 || h.BuildVersion != 418783 || h.SaveName != "only one way to find out_autosave_0" ||
		h.MapName != "Persistent_Level" || h.SessionName != "only one way to find out" || h.PlayedSeconds != 7200 || !h.SavedAt.Equal(at) {
		t.Errorf("header = %+v", h)
	}
}

// A 1.0 header (version 13) has no save name.
func TestSaveHeaderV13(t *testing.T) {
	h, err := readSaveHeader(bytes.NewReader(buildHeader(13, "ignored", "Home", 60, time.Unix(1700000000, 0))))
	if err != nil || h.SaveName != "" || h.SessionName != "Home" || h.SavedAt.Unix() != 1700000000 {
		t.Errorf("header = %+v, %v", h, err)
	}
}

// A name that isn't plain ASCII is written as UTF-16 with a negative length.
func TestSaveHeaderUTF16(t *testing.T) {
	var b []byte
	i32 := func(v int32) { b = binary.LittleEndian.AppendUint32(b, uint32(v)) }
	i32(14)
	i32(52)
	i32(1)
	i32(0) // save name
	i32(0) // map name
	i32(0) // map options
	name := utf16.Encode([]rune("Fábrica 🏭\x00"))
	i32(-int32(len(name)))
	for _, u := range name {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	i32(5)
	b = binary.LittleEndian.AppendUint64(b, uint64(timeTicks(time.Unix(0, 0))))
	h, err := readSaveHeader(bytes.NewReader(b))
	if err != nil || h.SessionName != "Fábrica 🏭" {
		t.Errorf("header = %+v, %v", h, err)
	}
}

func TestSaveHeaderNotASave(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("this is not a save file at all, it is text"),
		"truncated": buildHeader(14, "a", "b", 1, time.Now())[:30],
		"too long":  append(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 14), 52), 1), 1<<30), 'x'),
		"old":       binary.LittleEndian.AppendUint32(nil, 2),
	} {
		if _, err := readSaveHeader(bytes.NewReader(b)); !errors.Is(err, errNotSave) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTicks(t *testing.T) {
	at := time.Date(2026, 11, 26, 20, 15, 3, 412000000, time.UTC)
	if got := ticksTime(timeTicks(at)); !got.Equal(at) {
		t.Errorf("round trip: %v", got)
	}
	// The .NET epoch.
	if got := ticksTime(621355968000000000); !got.Equal(time.Unix(0, 0)) {
		t.Errorf("1970 = %v", got)
	}
	if !ticksTime(0).IsZero() {
		t.Errorf("0 ticks isn't the zero time")
	}
}

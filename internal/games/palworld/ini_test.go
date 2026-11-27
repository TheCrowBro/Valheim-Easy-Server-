package palworld

import (
	"os"
	"strings"
	"testing"
)

// The real DefaultPalWorldSettings.ini (1.0.5) reads back exactly: every
// option, including quoted text, an empty value and a bracketed list, and
// writing it again changes nothing.
func TestOptionsRealDefaultRoundTrip(t *testing.T) {
	f := parseINI(string(defaultSettings))
	if f.eol != "\r\n" {
		t.Errorf("the shipped file's line ends are %q, want CRLF", f.eol)
	}
	opts, ok := f.optionsLine()
	if !ok {
		t.Fatal("no OptionSettings line in the shipped default")
	}
	for key, want := range map[string]string{
		"RESTAPIEnabled":       "False",
		"RESTAPIPort":          "8212",
		"RCONEnabled":          "False",
		"ServerName":           `"Default Palworld Server"`,
		"ServerPassword":       `""`,
		"ServerPlayerMaxNum":   "32",
		"CrossplayPlatforms":   "(Steam,Xbox,PS5,Mac)",
		"DenyTechnologyList":   "",
		"LogFormatType":        "Text",
		"AutoSaveSpan":         "30.000000",
		"DeathPenalty":         "Item",
		"PublicPort":           "8211",
		"bIsUseBackupSaveData": "True",
	} {
		if got, ok := opts.get(key); !ok || got != want {
			t.Errorf("%s = %q (%v), want %q", key, got, ok, want)
		}
	}
	if len(opts) < 100 {
		t.Errorf("read %d options; the 1.0.5 file has more than 100", len(opts))
	}
	_, line := f.find(optionKey)
	if got := opts.String(); got != line {
		t.Errorf("writing the options back changed them:\n%s\n%s", got, line)
	}
	if got := f.String(); got != strings.TrimRight(string(defaultSettings), "\r\n")+"\r\n" {
		t.Errorf("writing the file back changed it")
	}
}

// Commas and brackets inside quoted text don't split an option; a key is
// found in any letter case and keeps its spelling when set; a new key goes
// at the end.
func TestOptionsQuotedAndNested(t *testing.T) {
	opts := parseOptions(`ServerName="A, (b)",X=(1,(2,3)),bFlag=True,Odd,Desc="say \"hi\", ok"`)
	if len(opts) != 5 {
		t.Fatalf("parsed %d options: %+v", len(opts), opts)
	}
	if v, _ := opts.get("servername"); v != `"A, (b)"` {
		t.Errorf("ServerName = %q", v)
	}
	if v, _ := opts.get("X"); v != "(1,(2,3))" {
		t.Errorf("X = %q", v)
	}
	if v, _ := opts.get("Desc"); unquote(v) != `say "hi", ok` {
		t.Errorf("Desc = %q", v)
	}
	opts = opts.set("SERVERNAME", `"New"`).set("RESTAPIPort", "9000")
	want := `(ServerName="New",X=(1,(2,3)),bFlag=True,Odd,Desc="say \"hi\", ok",RESTAPIPort=9000)`
	if got := opts.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A file's own line ends are kept; a byte order mark is dropped; a key
// with no section to go in gets one.
func TestINIFileEdits(t *testing.T) {
	f := parseINI("\uFEFF[A]\nx=1\n")
	f.setKey("[B]", "y", "2")
	if got := f.String(); got != "[A]\nx=1\n[B]\ny=2\n" {
		t.Errorf("LF file: %q", got)
	}
	f = parseINI("[/Script/Pal.PalGameLocalSettings]\r\nAudioSettings=(Master=0.5)\r\nDedicatedServerName=OLD\r\n")
	f.setKey(localSection, worldKey, "NEW")
	if got := f.String(); got != "[/Script/Pal.PalGameLocalSettings]\r\nAudioSettings=(Master=0.5)\r\nDedicatedServerName=NEW\r\n" {
		t.Errorf("CRLF file: %q", got)
	}
	f = parseINI("[/Script/Pal.PalGameLocalSettings]\r\nAudioSettings=(Master=0.5)\r\n")
	f.setKey(localSection, worldKey, "NEW")
	if got := f.String(); got != "[/Script/Pal.PalGameLocalSettings]\r\nDedicatedServerName=NEW\r\nAudioSettings=(Master=0.5)\r\n" {
		t.Errorf("key added under its section: %q", got)
	}
	if _, ok := parseINI("[/Script/Pal.PalGameWorldSettings]\r\n").optionsLine(); ok {
		t.Error("a file with only the section has no options line")
	}
	if _, ok := parseINI("OptionSettings=(A=1,B=").optionsLine(); ok {
		t.Error("a cut-short options line must not count as one")
	}
}

// A real dedicated server's GameUserSettings.ini (testdata, FACTS.md):
// the world choice is found, and changing it keeps every other line.
func TestRealGameUserSettings(t *testing.T) {
	b, err := os.ReadFile("testdata/ini/GameUserSettings-dedicated.ini")
	if err != nil {
		t.Fatal(err)
	}
	f := parseINI(string(b))
	if _, v := f.find(worldKey); v != "C8A34115A3414E9E8EE21C5D7BC7391E" || !reWorldID.MatchString(v) {
		t.Errorf("DedicatedServerName = %q", v)
	}
	before := len(f.lines)
	f.setKey(localSection, worldKey, "0123456789ABCDEF0123456789ABCDEF")
	if len(f.lines) != before || !strings.Contains(f.String(), "DedicatedServerName=0123456789ABCDEF0123456789ABCDEF") ||
		!strings.Contains(f.String(), "AntiAliasingType=AAM_TSR") {
		t.Errorf("after the change:\n%s", f.String())
	}
}

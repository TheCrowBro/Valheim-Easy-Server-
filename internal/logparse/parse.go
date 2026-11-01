// Package logparse understands the Valheim dedicated server log: who joins
// and leaves, the crossplay join code, saves, and common problems.
package logparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind is what a log line means.
type Kind int

const (
	Unknown     Kind = iota
	ServerReady      // the world is loaded and the server is open for players
	JoinCode
	PeerCount
	Connecting
	Handshake
	PlatformID
	Character
	Disconnect
	WrongPassword
	BadVersion
	Version
	WorldSaved
	WorldLoadFailed
	BadPasswordConfig
	Shutdown
	CrossplayFailed // Windows couldn't load Valheim's crossplay library
	SteamConnected  // logged in to Steam, which can come before the world has loaded
)

// Event is one parsed log line.
type Event struct {
	Kind       Kind
	Time       time.Time // from the line's timestamp; zero if it had none
	ID         string    // connection or socket ID
	PlatformID string    // e.g. Steam_7656119..., V_123..., Xbox_...
	Name       string    // character name
	Dead       bool      // a Character event for a death (ZDOID 0:0)
	Count      int       // players or connections; -1 if not given
	Code       string    // crossplay join code
	Session    string    // the server name a join code line is about
	Addr       string    // public address from the session line
	Version    string    // server version, or the remote version for BadVersion
	Mine       string    // our version for BadVersion
	Millis     float64   // save duration in ms, 0 if unknown
	Text       string    // the message without timestamp
}

// Valheim writes some of what a connecting player sends into its log: their
// game's version string (before it checks the password) and their
// character's name. So each message is recognised by how it starts, never by
// text it merely contains, and the messages that carry a player's text are
// read first and only as themselves.
var (
	reLevel        = regexp.MustCompile(`^\[[^\]]*\]\s*`) // BepInEx's "[Info   :   Unity Log] "
	reTimestamp    = regexp.MustCompile(`^(\d{1,2}/\d{1,2}/\d{4} \d{1,2}:\d{2}:\d{2}): ?`)
	reJoinActive   = regexp.MustCompile(`^Session "(.*)" with join code (\d*) and IP (\S*) is active with (\d+) player`)
	reJoinReg      = regexp.MustCompile(`^Session "(.*)" registered with join code (\d+)`)
	reNewSession   = regexp.MustCompile(`^New session server "(.*)" that has join code (\d*)\s*, now (\d+) player`)
	reConnections  = regexp.MustCompile(`^Connections (\d+) ZDOS:`)
	reGotConn      = regexp.MustCompile(`^Got connection SteamID (\d+)`)
	reHandshake    = regexp.MustCompile(`^Got handshake from client (\S+)`)
	rePlayFab      = regexp.MustCompile(`^PlayFab socket with remote ID (\S+) received local Platform ID (\S+)`)
	reCharacter    = regexp.MustCompile(`^Got character ZDOID from (.+?) : (-?\d+):(-?\d+)$`)
	reClosing      = regexp.MustCompile(`^Closing socket (\S+)`)
	rePeerWrongPw  = regexp.MustCompile(`^Peer (\S+) has wrong password`)
	rePeerIncompat = regexp.MustCompile(`^Peer (\S+) has incompatible version`)
	reWrongPw      = regexp.MustCompile(`(?i)wrong password`)
	reIncompat     = regexp.MustCompile(`(?i)incompatible version`)
	reMineRemote   = regexp.MustCompile(`mine:?\s*(?:l-)?(\d+(?:\.\d+)+).*?remote:?\s*(?:l-)?(\d+(?:\.\d+)+)`)
	reVerCheck     = regexp.MustCompile(`(?i)^version check their:\s*(?:l-)?(\S+)\s+mine:\s*(?:l-)?(\S+)`)
	reVersion      = regexp.MustCompile(`^Valheim version:\s*(?:l-)?(\d+(?:\.\d+){1,3})`)
	reSaved        = regexp.MustCompile(`^World saved \(\s*([\d.,]+)\s*ms\s*\)`)
	reSaveStep     = regexp.MustCompile(`^World save \((\d+)/(\d+)\) done`)
)

// Parse reads one log line.
func Parse(line string) Event {
	text := strings.TrimRight(line, "\r\n")
	ev := Event{Count: -1}
	if m := reLevel.FindString(text); m != "" {
		text = text[len(m):]
	}
	if m := reTimestamp.FindStringSubmatch(text); m != nil {
		if t, err := time.ParseInLocation("1/2/2006 15:04:05", m[1], time.Local); err == nil {
			ev.Time = t
		}
		text = text[len(m[0]):]
	}
	text = strings.TrimSpace(text)
	ev.Text = text

	// The messages that carry a player's text.
	switch {
	case strings.HasPrefix(text, "Got character ZDOID from "):
		if m := reCharacter.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Name = Character, strings.TrimSpace(m[1])
			ev.Dead = m[2] == "0" && m[3] == "0"
		}
		return ev
	case strings.HasPrefix(text, "Peer "):
		if m := rePeerWrongPw.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = WrongPassword, m[1]
		} else if m := rePeerIncompat.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = BadVersion, m[1]
			if m := reMineRemote.FindStringSubmatch(text); m != nil {
				ev.Mine, ev.Version = m[1], m[2]
			}
		}
		return ev
	case len(text) >= 14 && strings.EqualFold(text[:14], "VERSION check "):
		if m := reVerCheck.FindStringSubmatch(text); m != nil && m[1] != m[2] {
			ev.Kind, ev.Version, ev.Mine = BadVersion, m[1], m[2]
		}
		return ev
	case strings.HasPrefix(text, "PlayFab socket with remote ID "):
		if m := rePlayFab.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID, ev.PlatformID = PlatformID, m[1], m[2]
		}
		return ev
	case strings.HasPrefix(text, "Got handshake from client "):
		if m := reHandshake.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = Handshake, m[1]
			if isDigits(m[1]) {
				ev.PlatformID = "Steam_" + m[1]
			}
		}
		return ev
	}

	// The server's own messages.
	switch {
	case strings.HasPrefix(text, "Opened Steam server"), strings.HasPrefix(text, "Opened PlayFab server"):
		ev.Kind = ServerReady
	case strings.HasPrefix(text, "Game server connected"):
		ev.Kind = SteamConnected
	case strings.HasPrefix(text, "DllNotFoundException: Party"),
		strings.HasPrefix(text, "DLL Not Found: This error usually occurs when you do not have the correct dependencies installed, and will prevent crossplay"):
		ev.Kind = CrossplayFailed
	case strings.HasPrefix(text, "Session \""), strings.HasPrefix(text, "New session server \""):
		if m := reJoinActive.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Session, ev.Code, ev.Addr, ev.Count = JoinCode, m[1], m[2], m[3], atoi(m[4])
		} else if m := reJoinReg.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Session, ev.Code = JoinCode, m[1], m[2]
		} else if m := reNewSession.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Session, ev.Code, ev.Count = JoinCode, m[1], m[2], atoi(m[3])
		}
	case strings.HasPrefix(text, "Connections "):
		if m := reConnections.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Count = PeerCount, atoi(m[1])
		}
	case strings.HasPrefix(text, "Got connection SteamID "):
		if m := reGotConn.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID, ev.PlatformID = Connecting, m[1], "Steam_"+m[1]
		}
	case strings.HasPrefix(text, "Closing socket "):
		if m := reClosing.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = Disconnect, m[1]
		}
	case strings.HasPrefix(text, "Error bad password"):
		ev.Kind = BadPasswordConfig
	case reWrongPw.MatchString(text):
		ev.Kind = WrongPassword
	case reIncompat.MatchString(text):
		ev.Kind = BadVersion
		if m := reMineRemote.FindStringSubmatch(text); m != nil {
			ev.Mine, ev.Version = m[1], m[2]
		}
	case strings.HasPrefix(text, "Valheim version"):
		if m := reVersion.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Version = Version, m[1]
		}
	case strings.HasPrefix(text, "World saved"):
		ev.Kind = WorldSaved
		if m := reSaved.FindStringSubmatch(text); m != nil {
			ev.Millis, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
		}
	case strings.HasPrefix(text, "World save ("):
		if m := reSaveStep.FindStringSubmatch(text); m != nil && m[1] == m[2] {
			ev.Kind = WorldSaved
		}
	case strings.HasPrefix(text, "World load failed"):
		ev.Kind = WorldLoadFailed
	case strings.HasPrefix(text, "OnApplicationQuit"), strings.HasPrefix(text, "Net scene destroyed"),
		strings.HasPrefix(text, "Shuting down"), strings.HasPrefix(text, "Shutting down"):
		ev.Kind = Shutdown
	}
	return ev
}

// IsNoise reports lines that are Unity boilerplate rather than messages.
func IsNoise(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "(Filename:") ||
		strings.HasPrefix(t, "UnityEngine.StackTraceUtility:ExtractStackTrace") ||
		strings.HasPrefix(t, "UnityEngine.DebugLogHandler:") ||
		strings.HasPrefix(t, "UnityEngine.Logger:Log") ||
		strings.HasPrefix(t, "UnityEngine.Debug:Log")
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

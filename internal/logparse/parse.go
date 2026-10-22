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
	Unknown Kind = iota
	ServerReady
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
	Addr       string    // public address from the session line
	Version    string    // server version, or the remote version for BadVersion
	Mine       string    // our version for BadVersion
	Millis     float64   // save duration in ms, 0 if unknown
	Text       string    // the message without timestamp
}

var (
	reTimestamp   = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)?(\d{1,2}/\d{1,2}/\d{4} \d{1,2}:\d{2}:\d{2}): ?`)
	reJoinActive  = regexp.MustCompile(`Session "(.*)" with join code (\d*) and IP (\S*) is active with (\d+) player`)
	reJoinReg     = regexp.MustCompile(`registered with join code (\d+)`)
	reNewSession  = regexp.MustCompile(`New session server "(.*)" that has join code (\d*)\s*, now (\d+) player`)
	reConnections = regexp.MustCompile(`^Connections (\d+) ZDOS:`)
	reGotConn     = regexp.MustCompile(`Got connection SteamID (\d+)`)
	reHandshake   = regexp.MustCompile(`Got handshake from client (\S+)`)
	rePlayFab     = regexp.MustCompile(`PlayFab socket with remote ID (\S+) received local Platform ID (\S+)`)
	reCharacter   = regexp.MustCompile(`Got character ZDOID from (.+?) : (-?\d+):(-?\d+)`)
	reClosing     = regexp.MustCompile(`Closing socket (\S+)`)
	rePeerWrongPw = regexp.MustCompile(`Peer (\S+) has wrong password`)
	reWrongPw     = regexp.MustCompile(`(?i)wrong password`)
	reIncompat    = regexp.MustCompile(`(?i)incompatible version`)
	reMineRemote  = regexp.MustCompile(`mine:?\s*(?:l-)?(\d+(?:\.\d+)+).*?remote:?\s*(?:l-)?(\d+(?:\.\d+)+)`)
	rePeerID      = regexp.MustCompile(`Peer (\S+)`)
	reVerCheck    = regexp.MustCompile(`(?i)version check their:\s*(?:l-)?(\S+)\s+mine:\s*(?:l-)?(\S+)`)
	reVersion     = regexp.MustCompile(`Valheim version:\s*(?:l-)?(\d+(?:\.\d+){1,3})`)
	reSaved       = regexp.MustCompile(`World saved \(\s*([\d.,]+)\s*ms\s*\)`)
	reSaveStep    = regexp.MustCompile(`World save \((\d+)/(\d+)\) done`)
)

// Parse reads one log line.
func Parse(line string) Event {
	text := strings.TrimRight(line, "\r\n")
	ev := Event{Count: -1}
	if m := reTimestamp.FindStringSubmatch(text); m != nil {
		if t, err := time.ParseInLocation("1/2/2006 15:04:05", m[1], time.Local); err == nil {
			ev.Time = t
		}
		text = text[len(m[0]):]
	}
	text = strings.TrimSpace(text)
	ev.Text = text

	switch {
	case strings.Contains(text, "Game server connected"):
		ev.Kind = ServerReady
	case strings.Contains(text, "join code"):
		if m := reJoinActive.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Code, ev.Addr, ev.Count = JoinCode, m[2], m[3], atoi(m[4])
		} else if m := reJoinReg.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Code = JoinCode, m[1]
		} else if m := reNewSession.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Code, ev.Count = JoinCode, m[2], atoi(m[3])
		}
	case strings.HasPrefix(text, "Connections "):
		if m := reConnections.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Count = PeerCount, atoi(m[1])
		}
	case strings.Contains(text, "Got connection SteamID"):
		if m := reGotConn.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID, ev.PlatformID = Connecting, m[1], "Steam_"+m[1]
		}
	case strings.Contains(text, "Got handshake from client"):
		if m := reHandshake.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = Handshake, m[1]
			if isDigits(m[1]) {
				ev.PlatformID = "Steam_" + m[1]
			}
		}
	case strings.Contains(text, "received local Platform ID"):
		if m := rePlayFab.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID, ev.PlatformID = PlatformID, m[1], m[2]
		}
	case strings.Contains(text, "Got character ZDOID from"):
		if m := reCharacter.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Name = Character, strings.TrimSpace(m[1])
			ev.Dead = m[2] == "0" && m[3] == "0"
		}
	case strings.Contains(text, "Closing socket"):
		if m := reClosing.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.ID = Disconnect, m[1]
		}
	case strings.Contains(text, "Error bad password"):
		ev.Kind = BadPasswordConfig
	case reWrongPw.MatchString(text):
		ev.Kind = WrongPassword
		if m := rePeerWrongPw.FindStringSubmatch(text); m != nil {
			ev.ID = m[1]
		}
	case reIncompat.MatchString(text):
		ev.Kind = BadVersion
		if m := reMineRemote.FindStringSubmatch(text); m != nil {
			ev.Mine, ev.Version = m[1], m[2]
		}
		if m := rePeerID.FindStringSubmatch(text); m != nil {
			ev.ID = m[1]
		}
	case reVerCheck.MatchString(text):
		m := reVerCheck.FindStringSubmatch(text)
		if m[1] != m[2] {
			ev.Kind, ev.Version, ev.Mine = BadVersion, m[1], m[2]
		}
	case strings.Contains(text, "Valheim version"):
		if m := reVersion.FindStringSubmatch(text); m != nil {
			ev.Kind, ev.Version = Version, m[1]
		}
	case strings.Contains(text, "World saved"):
		ev.Kind = WorldSaved
		if m := reSaved.FindStringSubmatch(text); m != nil {
			ev.Millis, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
		}
	case strings.Contains(text, "World save ("):
		if m := reSaveStep.FindStringSubmatch(text); m != nil && m[1] == m[2] {
			ev.Kind = WorldSaved
		}
	case strings.Contains(text, "World load failed"):
		ev.Kind = WorldLoadFailed
	case strings.Contains(text, "OnApplicationQuit"), strings.Contains(text, "Net scene destroyed"),
		strings.Contains(text, "Shuting down"), strings.Contains(text, "Shutting down"):
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

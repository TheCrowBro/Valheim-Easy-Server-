package routerports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// SteamServer is one game server Steam knows at an address, from
// ISteamApps/GetServersAtAddress.
type SteamServer struct {
	Addr     string `json:"addr"`     // "203.0.113.25:2457": its query address
	GamePort int    `json:"gameport"` // the port players join on
	AppID    int    `json:"appid"`    // the game's Steam app id (892970 is Valheim)
	GameDir  string `json:"gamedir"`  // "valheim"
	SteamID  string `json:"steamid"`
	Secure   bool   `json:"secure"`
	LAN      bool   `json:"lan"`
}

// steamURL is Steam's list of servers by address; the tests point it at a
// stand-in.
var steamURL = "https://api.steampowered.com/ISteamApps/GetServersAtAddress/v1/"

// steamClient asks Steam, through a proxy when the computer has one set,
// as any other request to the internet.
var steamClient = &http.Client{Timeout: 10 * time.Second}

// SteamSees asks Steam which game servers it knows at publicIP
// (api.steampowered.com/ISteamApps/GetServersAtAddress/v1/?addr=IP; no key
// needed). A game that registers with Steam is reachable from the internet
// when its server is listed.
func SteamSees(ctx context.Context, publicIP string) ([]SteamServer, error) {
	ip := net.ParseIP(publicIP)
	if ip == nil {
		return nil, fmt.Errorf("%q isn't an internet address", publicIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamURL+"?"+url.Values{"addr": {ip.String()}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := steamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Steam answered %s", resp.Status)
	}
	var answer struct {
		Response struct {
			Success bool          `json:"success"`
			Message string        `json:"message"`
			Servers []steamServer `json:"servers"`
		} `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("Steam's answer couldn't be read: %w", err)
	}
	if !answer.Response.Success {
		msg := answer.Response.Message
		if msg == "" {
			msg = "no reason given"
		}
		return nil, errors.New("Steam couldn't list the servers: " + msg)
	}
	out := []SteamServer{}
	for _, s := range answer.Response.Servers {
		srv := s.SteamServer
		// Steam writes the id as a string; a number is read too, in case
		// (as written: 17 digits don't fit a float).
		var id json.Number
		if json.Unmarshal(s.ID, &id) == nil {
			srv.SteamID = id.String()
		}
		out = append(out, srv)
	}
	return out, nil
}

// steamServer is SteamServer as Steam writes it.
type steamServer struct {
	SteamServer
	ID json.RawMessage `json:"steamid"`
}

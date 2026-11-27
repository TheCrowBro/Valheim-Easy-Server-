package routerports

// UPnP IGD, versions 1 and 2: once SSDP has found the router, its
// description (an XML file on the router) lists its services, and the
// WAN connection service takes SOAP calls (an XML request over HTTP POST)
// to add, look up and delete port mappings and to say the router's
// internet address.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// connServices are the WAN connection services that forward ports, best
// first: IGD version 2's, then version 1's for a cable or fibre line, then
// version 1's for a DSL line (PPP).
var connServices = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// upnpRouter is a router's WAN connection service.
type upnpRouter struct {
	control string // the service's control URL, on the router
	service string // its type, one of connServices
	client  *http.Client
}

func (u *upnpRouter) Kind() string { return "upnp" }

// version is the IGD version of the service: 2 has AddAnyPortMapping.
func (u *upnpRouter) version() int {
	if strings.HasSuffix(u.service, ":2") {
		return 2
	}
	return 1
}

// findUPnP searches the network and returns the first router whose
// connection service says its internet address, or failing that the first
// whose service answers at all (a router whose line is down still forwards
// ports once it is up); nil when none.
//
// A service that answers 0.0.0.0 may be connected to nothing: a DSL router
// often lists an unused WANIPConnection next to the PPP connection that
// carries its traffic, and the unused one answers like that. Ports
// forwarded on it would say "open" while friends outside still couldn't
// join, so it only counts as the fallback, like a service that refuses
// (see Connected for the other reasons a router says 0.0.0.0, when the
// fallback is the right one).
func (f finder) findUPnP(ctx context.Context, lan, gateway net.IP) *upnpRouter {
	var fallback *upnpRouter
	for _, a := range f.search(ctx, lan, gateway) {
		for _, r := range f.services(ctx, a.location) {
			ip, err := r.ExternalIP(ctx)
			if err == nil && Connected(ip) {
				return r
			}
			var ue *upnpError
			if fallback == nil && (err == nil || errors.As(err, &ue)) {
				fallback = r
			}
		}
	}
	return fallback
}

// The parts of a UPnP device description that matter here.
type upnpDescription struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

type upnpDevice struct {
	DeviceType string        `xml:"deviceType"`
	Services   []upnpService `xml:"serviceList>service"`
	Devices    []upnpDevice  `xml:"deviceList>device"`
}

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// services reads the description at loc and returns its connection
// services in the order of connServices. Their control URLs must be on the
// same device as the description.
func (f finder) services(ctx context.Context, loc *url.URL) []*upnpRouter {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
	if err != nil {
		return nil
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var d upnpDescription
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil
	}
	base := loc
	if d.URLBase != "" {
		if b, err := url.Parse(strings.TrimSpace(d.URLBase)); err == nil && b.Hostname() == loc.Hostname() {
			base = b
		}
	}
	var all []upnpService
	var walk func(dev upnpDevice)
	walk = func(dev upnpDevice) {
		all = append(all, dev.Services...)
		for _, sub := range dev.Devices {
			walk(sub)
		}
	}
	walk(d.Device)
	var out []*upnpRouter
	for _, want := range connServices {
		for _, s := range all {
			if strings.TrimSpace(s.ServiceType) != want {
				continue
			}
			ctl, err := base.Parse(strings.TrimSpace(s.ControlURL))
			if err != nil || ctl.Scheme != "http" || ctl.Hostname() != loc.Hostname() {
				continue
			}
			out = append(out, &upnpRouter{control: ctl.String(), service: want, client: f.client})
		}
	}
	return out
}

// upnpError is the router's refusal of a SOAP call: a UPnP error code
// (718 is "ConflictInMappingEntry") and its description.
type upnpError struct {
	Code int
	Desc string
}

func (e *upnpError) Error() string {
	return fmt.Sprintf("the router refused (UPnP error %d %s)", e.Code, e.Desc)
}

// The UPnP error codes the app acts on.
const (
	upnpNoSuchEntry     = 714 // NoSuchEntryInArray: there's no such mapping
	upnpConflict        = 718 // ConflictInMappingEntry: the port is already forwarded
	upnpPermanentOnly   = 725 // OnlyPermanentLeasesSupported: the lease must be 0
	upnpOtherMechanisms = 729 // ConflictWithOtherMechanisms: forwarded by another means (by hand)
)

// arg is one argument of a SOAP call, in the order the service's
// description gives (some routers refuse them in another order).
type arg struct{ name, value string }

// call makes one SOAP call and returns the reply's values by name.
func (u *upnpRouter) call(ctx context.Context, action string, args ...arg) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\r\n")
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	b.WriteString(`<s:Body><u:` + action + ` xmlns:u="` + u.service + `">`)
	for _, a := range args {
		b.WriteString("<" + a.name + ">" + html.EscapeString(a.value) + "</" + a.name + ">")
	}
	b.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.control, strings.NewReader(b.String()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+u.service+"#"+action+`"`)
	req.Close = true // some routers mishandle a second request on the same connection
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	values := soapValues(body)
	if code, err := strconv.Atoi(values["errorCode"]); err == nil {
		return nil, &upnpError{Code: code, Desc: values["errorDescription"]}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the router answered %s to %s", resp.Status, action)
	}
	return values, nil
}

// soapValues reads every element that holds only text in a SOAP reply, by
// its name without the namespace: a reply's values, or a fault's
// errorCode and errorDescription.
func soapValues(body []byte) map[string]string {
	out := map[string]string{}
	d := xml.NewDecoder(bytes.NewReader(body))
	var name string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name = t.Name.Local
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if name == t.Name.Local {
				out[name] = strings.TrimSpace(text.String())
			}
			name = ""
		}
	}
}

func (u *upnpRouter) ExternalIP(ctx context.Context) (net.IP, error) {
	v, err := u.call(ctx, "GetExternalIPAddress")
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(v["NewExternalIPAddress"]).To4()
	if ip == nil {
		return nil, errors.New("the router didn't say its internet address")
	}
	return ip, nil
}

// upnpProto is the protocol as UPnP writes it.
func upnpProto(p string) string { return strings.ToUpper(p) }

func (u *upnpRouter) add(ctx context.Context, action string, lan net.IP, m Mapping, secs int) (map[string]string, error) {
	return u.call(ctx, action,
		arg{"NewRemoteHost", ""},
		arg{"NewExternalPort", strconv.Itoa(m.Port)},
		arg{"NewProtocol", upnpProto(m.Proto)},
		arg{"NewInternalPort", strconv.Itoa(m.Port)},
		arg{"NewInternalClient", lan.String()},
		arg{"NewEnabled", "1"},
		arg{"NewPortMappingDescription", m.Desc},
		arg{"NewLeaseDuration", strconv.Itoa(secs)})
}

// Map adds the mapping (which renews one that is already there). A router
// that only keeps mappings until it restarts (UPnP error 725) gets one
// without a lease: the app removes it when the game stops. When the port
// is already forwarded (718), the existing entry says whether it is this
// computer's (ErrForwardedByHand, or the app's own from before, which is
// made again) or another's (ErrConflict). IGD version 2 routers that
// refuse AddPortMapping for another reason are asked with
// AddAnyPortMapping, which only counts when the router keeps the port.
func (u *upnpRouter) Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error {
	if lan.To4() == nil {
		return errors.New("this computer's address on the home network isn't known")
	}
	secs := int(lease / time.Second)
	_, err := u.add(ctx, "AddPortMapping", lan, m, secs)
	if code := upnpCode(err); code == upnpPermanentOnly && secs != 0 {
		secs = 0
		_, err = u.add(ctx, "AddPortMapping", lan, m, secs)
	}
	switch code := upnpCode(err); {
	case err == nil:
		return nil
	case code == upnpConflict || code == upnpOtherMechanisms:
		return u.conflict(ctx, lan, m, secs, err)
	case code != 0 && u.version() >= 2:
		v, err2 := u.add(ctx, "AddAnyPortMapping", lan, m, secs)
		if err2 != nil {
			return err
		}
		got, _ := strconv.Atoi(v["NewReservedPort"])
		if got == m.Port {
			return nil
		}
		// The router picked another port: friends would need to be told a
		// different one, so it doesn't count. Take it back.
		if got > 0 {
			u.Unmap(ctx, lan, Mapping{Proto: m.Proto, Port: got, Desc: m.Desc})
		}
		return fmt.Errorf("%w (the router offered port %d instead)", ErrConflict, got)
	}
	return err
}

// conflict works out a refused mapping from the entry the router already
// has for the port.
func (u *upnpRouter) conflict(ctx context.Context, lan net.IP, m Mapping, secs int, refused error) error {
	v, err := u.call(ctx, "GetSpecificPortMappingEntry",
		arg{"NewRemoteHost", ""},
		arg{"NewExternalPort", strconv.Itoa(m.Port)},
		arg{"NewProtocol", upnpProto(m.Proto)})
	if err != nil {
		// A forward made on the router's own page often isn't in its UPnP
		// list, so nothing says where it goes.
		return fmt.Errorf("%w: %v", ErrConflict, refused)
	}
	client := net.ParseIP(v["NewInternalClient"])
	port, _ := strconv.Atoi(v["NewInternalPort"])
	if client == nil || !client.Equal(lan) || port != m.Port {
		where := v["NewInternalClient"]
		if port != 0 {
			where = net.JoinHostPort(where, strconv.Itoa(port))
		}
		return fmt.Errorf("%w: it goes to %s", ErrConflict, where)
	}
	if v["NewPortMappingDescription"] != m.Desc {
		return ErrForwardedByHand
	}
	// The app's own mapping, from before (say, the app restarted), that
	// the router won't renew in place: make it again, so the lease starts
	// over.
	if err := u.Unmap(ctx, lan, m); err != nil {
		return err
	}
	_, err = u.add(ctx, "AddPortMapping", lan, m, secs)
	return err
}

func (u *upnpRouter) Unmap(ctx context.Context, lan net.IP, m Mapping) error {
	_, err := u.call(ctx, "DeletePortMapping",
		arg{"NewRemoteHost", ""},
		arg{"NewExternalPort", strconv.Itoa(m.Port)},
		arg{"NewProtocol", upnpProto(m.Proto)})
	if upnpCode(err) == upnpNoSuchEntry {
		return nil
	}
	return err
}

// upnpCode is err's UPnP error code, or 0.
func upnpCode(err error) int {
	var ue *upnpError
	if errors.As(err, &ue) {
		return ue.Code
	}
	return 0
}

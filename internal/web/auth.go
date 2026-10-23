package web

import (
	"net"
	"net/http"
	"strings"
)

const blockedMsg = "This request was blocked for safety. Reload the page and try again."

// guard lets in only the control panel on this computer. Anyone sitting at
// the computer may use it, so there's no password; what it keeps out is
// other websites open in a browser here:
//   - the connection must come from this computer and name it as the host
//     (127.0.0.1, localhost or ::1), which stops a site that points its own
//     name at 127.0.0.1;
//   - API requests the browser marks as coming from another site are
//     refused, so other sites can't read or start downloads;
//   - changes need the panel's own X-VES header, which other sites can't
//     add, and a matching Origin when the browser sends one.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) || !loopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, "The control panel only works on the server computer itself.")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
				writeErr(w, http.StatusForbidden, blockedMsg)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
				writeErr(w, http.StatusForbidden, blockedMsg)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHost reports whether a Host header names this computer.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin checks a change came from the panel: a page on another site
// can't add our custom header without permission, and if the browser sends
// an Origin it must be this panel.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-VES") != "1" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return strings.EqualFold(o, r.Host)
}

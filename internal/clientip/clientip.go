// Package clientip determines the visitor's IP address for a request that
// reached the server through Cloudflare -> load balancer -> Traefik.
//
// The socket address (r.RemoteAddr) is the ingress pod, and X-Real-IP is set
// by Traefik to its own peer (the Cloudflare edge), so neither identifies the
// visitor. The sources are tried in this order by default:
//
//  1. CF-Connecting-IP
//  2. X-Forwarded-For (first entry only)
//  3. X-Real-IP
//  4. r.RemoteAddr (always the final fallback)
//
// The header list can be overridden with the CLIENT_IP_HEADERS environment
// variable (comma-separated, case-insensitive); unset, empty or only
// whitespace/commas means the default. For every header, including
// X-Forwarded-For, the first comma-separated entry is used. Values that are
// empty, "unknown" or not an IP address are skipped; ports are stripped.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
)

// EnvVar is the environment variable that overrides the header order.
const EnvVar = "CLIENT_IP_HEADERS"

// DefaultHeaders is the header order used when CLIENT_IP_HEADERS is unset.
var DefaultHeaders = []string{"cf-connecting-ip", "x-forwarded-for", "x-real-ip"}

var (
	envOnce    sync.Once
	envHeaders []string
)

// FromRequest returns the client IP for r using the header order from
// CLIENT_IP_HEADERS (read once) or DefaultHeaders.
func FromRequest(r *http.Request) string {
	envOnce.Do(func() { envHeaders = HeadersFromEnv() })
	return FromRequestWith(r, envHeaders)
}

// HeadersFromEnv returns the header order from CLIENT_IP_HEADERS, or
// DefaultHeaders when it is unset or empty.
func HeadersFromEnv() []string {
	return ParseHeaders(os.Getenv(EnvVar))
}

// ParseHeaders turns a comma-separated header list into header names. An
// empty list, or one of only whitespace and commas, yields DefaultHeaders.
func ParseHeaders(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return DefaultHeaders
	}
	return out
}

// FromRequestWith returns the first valid IP found in the given headers, in
// order, falling back to r.RemoteAddr. If even RemoteAddr is not a valid IP
// it is returned unchanged.
func FromRequestWith(r *http.Request, headers []string) string {
	for _, name := range headers {
		// Join repeated header lines so "first entry" means the first value
		// overall, not the first value of the last line.
		if ip, ok := parse(firstEntry(strings.Join(r.Header.Values(name), ","))); ok {
			return ip
		}
	}
	if ip, ok := parse(r.RemoteAddr); ok {
		return ip
	}
	return r.RemoteAddr
}

func firstEntry(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

// parse validates s as an IP address, accepting "ip", "ip:port",
// "[ipv6]" and "[ipv6]:port". IPv4-mapped IPv6 addresses are unmapped.
func parse(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "unknown") {
		return "", false
	}
	if addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")); err == nil {
		return addr.Unmap().WithZone("").String(), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap().WithZone("").String(), true
		}
	}
	return "", false
}

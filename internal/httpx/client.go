package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP extracts a bare, parseable client IP address for a request.
//
// X-Forwarded-For is honored only when the immediate peer (RemoteAddr) is a
// trusted proxy — a loopback or private-network address — so a client connecting
// directly from the public internet cannot spoof the header to evade IP-based
// rate limiting. Behind a reverse proxy or load balancer on a private network the
// forwarded client address is used; a direct public peer is keyed by its real
// address. The result never contains a port or a comma-separated list.
func ClientIP(r *http.Request) string {
	peer := remoteIP(r.RemoteAddr)
	if peer != "" && isTrustedProxy(peer) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return first
			}
		}
	}
	return peer
}

// remoteIP strips the port from a RemoteAddr, tolerating a bare address.
func remoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	if ip := net.ParseIP(remoteAddr); ip != nil {
		return remoteAddr
	}
	return ""
}

// isTrustedProxy reports whether an immediate peer may set the forwarded client
// address: loopback, link-local, or RFC 1918/ULA private ranges.
func isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast()
}

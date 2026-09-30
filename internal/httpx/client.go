package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP extracts a bare IP address for a request, preferring the first
// X-Forwarded-For entry when the service sits behind a proxy.
//
// It deliberately returns only a parseable address: rate limiting keys and
// session records must not be built from a comma-separated list or a value with a
// port attached. Callers must not treat the result as trustworthy identity
// unless the deployment strips client-supplied forwarding headers at the edge.
func ClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	if ip := net.ParseIP(r.RemoteAddr); ip != nil {
		return r.RemoteAddr
	}
	return ""
}

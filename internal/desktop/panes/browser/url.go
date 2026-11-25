package browserpane

import (
	"net"
	"net/url"
	"strings"
)

// normalizeURL makes what's typed in an address bar a URL: one with a
// scheme as it is, a host and maybe a path over https, or http for this
// machine's.
func NormalizeURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return "", false
	}
	if u, err := url.Parse(s); err == nil {
		switch scheme := strings.ToLower(u.Scheme); {
		case scheme == "about" || scheme == "file":
			return s, true
		case (scheme == "http" || scheme == "https") && u.Host != "":
			return s, true
		}
	}
	host := s
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "http://" + s, true
	case net.ParseIP(host) != nil:
		if ip := net.ParseIP(host); ip.IsLoopback() || ip.IsPrivate() {
			return "http://" + s, true
		}
		return "https://" + s, true
	case strings.Contains(host, ".") && !strings.HasPrefix(host, ".") && !strings.HasSuffix(host, "."):
		return "https://" + s, true
	}
	return "", false
}

// pageHost is the host of a browser tab's page, for where it is.
func PageHost(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

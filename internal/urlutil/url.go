package urlutil

import (
	"net/url"
	"strings"
)

func WithNonLocalPortStripped(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	StripNonLocalPort(u)
	return u.String()
}

func StripNonLocalPort(u *url.URL) {
	if u == nil || u.Port() == "" {
		return
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return
	}
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
		return
	}
	u.Host = host
}

package app

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

func remoteIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func (a *App) clientIP(r *http.Request) netip.Addr {
	peer := remoteIP(r)
	if !trustedPeer(a.cfg.TrustedProxies, peer) {
		return peer
	}
	if addr, ok := parseAddr(r.Header.Get("CF-Connecting-IP")); ok {
		return addr
	}
	if addr, ok := forwardedFor(r.Header.Values("X-Forwarded-For"), a.cfg.TrustedProxies); ok {
		return addr
	}
	if addr, ok := parseAddr(r.Header.Get("X-Real-IP")); ok {
		return addr
	}
	return peer
}

func (a *App) clientIPString(r *http.Request) string {
	addr := a.clientIP(r)
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

func (a *App) clientIPValue(r *http.Request) any {
	addr := a.clientIP(r)
	if !addr.IsValid() {
		return nil
	}
	return addr.String()
}

func trustedPeer(trusted []netip.Prefix, peer netip.Addr) bool {
	if !peer.IsValid() || len(trusted) == 0 {
		return false
	}
	for _, prefix := range trusted {
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

func parseAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func forwardedFor(headers []string, trusted []netip.Prefix) (netip.Addr, bool) {
	hops := make([]string, 0, 8)
	for _, header := range headers {
		for part := range strings.SplitSeq(header, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	for _, hop := range slices.Backward(hops) {
		addr, ok := parseAddr(hop)
		if !ok {
			return netip.Addr{}, false
		}
		if !trustedPeer(trusted, addr) {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

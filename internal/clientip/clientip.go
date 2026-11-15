// Package clientip finds the address of the client behind the proxies this
// deployment runs, from the TCP peer and X-Forwarded-For. Everything that
// keys on an address (the login throttles, the register / resend / forgot
// limits, Turnstile's remoteip, logs) uses Resolver.IP, so they all agree.
//
// In production a request passes an edge reverse proxy, a tunnel and the
// cluster's ingress controller before it reaches the api. Every hop
// appends the address it received the connection from to X-Forwarded-For,
// so the header reads, left to right: whatever the client claimed, then
// what each trusted proxy saw. Only the right-hand part, written by proxies
// we run, is believed: the walk starts at the TCP peer and goes left while
// the address is a trusted proxy; the first untrusted one is the client.
// The leftmost entry is never used just because it is leftmost: the client
// writes it.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver resolves client addresses. The zero value trusts no proxy: the
// client is always the TCP peer and X-Forwarded-For is ignored.
type Resolver struct {
	trusted []netip.Prefix
}

// Parse builds a Resolver from TRUSTED_PROXIES: a comma-separated list of
// CIDRs ("10.8.0.1/32, 10.244.0.0/16"); a bare address means just that
// address. Empty trusts nothing (local development: the peer is the
// client). Any malformed entry is an error, so a typo fails at startup
// instead of silently trusting nothing or too much.
func Parse(list string) (*Resolver, error) {
	r := &Resolver{}
	for _, f := range strings.Split(list, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(f, "/") {
			p, err = netip.ParsePrefix(f)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(f); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not a CIDR or an IP address: %w", f, err)
		}
		if strings.Contains(f, "%") {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q has a zone", f)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

// Trusts reports whether any proxy is trusted (TRUSTED_PROXIES not empty).
func (r *Resolver) Trusts() bool { return r != nil && len(r.trusted) > 0 }

// IP is the client's address for req, as text ("203.0.113.7", "2001:db8::1").
//
//   - The TCP peer is not a trusted proxy (or none is configured):
//     X-Forwarded-For is ignored entirely and the peer is the client.
//   - Otherwise X-Forwarded-For is walked from the right, skipping trusted
//     proxies; the first address outside them is the client.
//   - A malformed entry stops the walk: the client is then the last trusted
//     address reached (the TCP peer, or the hop right of the garbage), never
//     anything left of it.
//   - Every entry trusted: the leftmost one, the furthest a trusted proxy
//     vouches for.
func (r *Resolver) IP(req *http.Request) string {
	peer, ok := parseAddr(req.RemoteAddr, true)
	if !ok {
		return req.RemoteAddr // not host:port (a test, a unix socket): as it is
	}
	if !r.isTrusted(peer) {
		return peer.String()
	}
	hops := forwardedHops(req.Header.Values("X-Forwarded-For"))
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(hops[i], false)
		if !ok {
			return client.String()
		}
		client = a
		if !r.isTrusted(a) {
			return a.String()
		}
	}
	return client.String()
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	if r == nil {
		return false
	}
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// forwardedHops splits every X-Forwarded-For header line (a proxy may add a
// line rather than extend the last one) into its entries, in order.
func forwardedHops(lines []string) []string {
	var hops []string
	for _, l := range lines {
		for _, h := range strings.Split(l, ",") {
			hops = append(hops, strings.TrimSpace(h))
		}
	}
	return hops
}

// parseAddr reads an address, unmapping IPv4-in-IPv6 so "::ffff:10.0.0.1"
// matches 10.0.0.0/8. withPort: "host:port" (RemoteAddr); otherwise a bare
// address as X-Forwarded-For carries it, a bracketed or ported form
// accepted too. Zones are refused.
func parseAddr(s string, withPort bool) (netip.Addr, bool) {
	var host string
	switch ap, err := netip.ParseAddrPort(s); {
	case withPort:
		h, _, err := net.SplitHostPort(s)
		if err != nil {
			return netip.Addr{}, false
		}
		host = h
	case err == nil:
		return unmap(ap.Addr())
	default:
		host = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return unmap(a)
}

func unmap(a netip.Addr) (netip.Addr, bool) {
	if a.Zone() != "" || !a.IsValid() {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

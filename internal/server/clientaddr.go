package server

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// clientResolver says which address a request really came from, for the one
// feature that needs it (counting opens). This file is the only non-test code
// that reads the connecting address or names X-Forwarded-For, and the fitness
// rules keep it so: a client controls every header it sends, so a header is
// believed only from a peer the operator listed in HBAND_TRUSTED_PROXIES, and
// then only by the rule below.
//
// Behind the VPS proxy the connecting peer is the proxy for every visitor, so
// the visitor is read from X-Forwarded-For, which each proxy appends its own peer
// to. The list is walked from the right: an entry a trusted proxy wrote is
// stepped over, and the first entry nobody trusted is the client. Entries to its
// left are whatever the client sent and are never read. X-Real-IP and Forwarded
// are never read, and neither are scheme and host: those come from
// HBAND_BASE_URL.
//
// A peer that is not trusted is the client, whatever its headers say. So with
// HBAND_TRUSTED_PROXIES missing or wrong, every visitor behind the proxy is
// counted as the proxy, and that is logged once per resolver, when such a peer
// sends X-Forwarded-For. A request is not counted, and is not keyed on the proxy,
// when a trusted peer forwarded no client entry (a proxy that sends no
// X-Forwarded-For, or one whose every entry is trusted) or when the client
// resolves to an internal address (a proxy of the chain that is missing from the
// list); each of those is logged once per resolver. A forwarded entry that is not
// an address is not counted either, and is not logged. Each resolver belongs to
// one app, and no warning carries an address.
type clientResolver struct {
	trusted []netip.Prefix
	// log is where the warnings go; slog.Default when nil.
	log *slog.Logger

	untrusted, noEntry, internal sync.Once
}

func newClientResolver(trusted []netip.Prefix, log *slog.Logger) *clientResolver {
	return &clientResolver{trusted: trusted, log: log}
}

func (c *clientResolver) warn(once *sync.Once, msg string) {
	once.Do(func() {
		l := c.log
		if l == nil {
			l = slog.Default()
		}
		l.Warn(msg)
	})
}

func (c *clientResolver) isTrusted(a netip.Addr) bool {
	for _, p := range c.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// cgnat is the shared address space (RFC 6598) overlay networks use. netip does
// not call it private.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// internalAddr reports whether a is an address no public visitor has: private,
// loopback, link-local, unspecified, or shared (CGNAT) space.
func internalAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || cgnat.Contains(a)
}

// addr returns the client of the request and true, or false when the request is
// not to be counted: the connecting address does not parse, or the client cannot
// be told (see the type's comment).
func (c *clientResolver) addr(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer := ap.Addr().Unmap().WithZone("")
	lines := r.Header.Values("X-Forwarded-For")

	if !c.isTrusted(peer) {
		// Whatever the header says, a peer nobody listed may have written it.
		if len(lines) > 0 {
			c.warn(&c.untrusted, "a request carried X-Forwarded-For from a peer that is not in HBAND_TRUSTED_PROXIES; the header is ignored, so if the reverse proxy is that peer, list it there or visitors are counted as the proxy")
		}
		return peer, true
	}

	entries := strings.Split(strings.Join(lines, ","), ",")
	for i := len(entries) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(entries[i])
		if entry == "" && len(entries) == 1 {
			break // no header, or an empty one: no entry at all
		}
		a, ok := parseForwarded(entry)
		if !ok {
			return netip.Addr{}, false
		}
		if c.isTrusted(a) {
			continue
		}
		if internalAddr(a) {
			c.warn(&c.internal, "X-Forwarded-For resolved to an internal address; is a proxy of the chain missing from HBAND_TRUSTED_PROXIES? Opens are not being counted")
			return netip.Addr{}, false
		}
		return a, true
	}
	c.warn(&c.noEntry, "a peer in HBAND_TRUSTED_PROXIES sent no X-Forwarded-For client entry; opens are not being counted until the proxy forwards the visitor's address")
	return netip.Addr{}, false
}

// parseForwarded reads one X-Forwarded-For entry: a bare address, ip:port or
// [v6]:port. A zone is dropped and an IPv4-mapped IPv6 address is the IPv4 one.
// Anything else (a name, "unknown", an obfuscated token, a bare bracket) is not an
// address.
func parseForwarded(entry string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(entry); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(entry); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

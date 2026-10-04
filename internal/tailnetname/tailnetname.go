// Package tailnetname turns Tailscale addresses into machine names using the
// MagicDNS resolver. Lookups run in the background and are cached, so callers
// on a request path never wait on DNS.
package tailnetname

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// magicDNS is the Tailscale resolver every tailnet node can reach.
	magicDNS = "100.100.100.100:53"

	successTTL = time.Hour
	failureTTL = 5 * time.Minute
	// lookupTimeout bounds one reverse lookup against MagicDNS. It is not an
	// upstream model connection.
	lookupTimeout = 3 * time.Second
	// maxEntries caps the cache and maxLookups the lookups in flight. A new
	// address that finds either full gets no name until room frees up.
	maxEntries = 1024
	maxLookups = 8
)

// Prefixes are the Tailscale address ranges (CGNAT IPv4 and the Tailscale ULA
// IPv6 block).
var Prefixes = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),
	mustCIDR("fd7a:115c:a1e0::/48"),
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

// IsTailnet reports whether ip is a Tailscale address.
func IsTailnet(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, prefix := range Prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// ShortName returns the first label of a DNS name ("mbp-m3.tail.ts.net." is
// "mbp-m3").
func ShortName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if label, _, ok := strings.Cut(name, "."); ok {
		return label
	}
	return name
}

type entry struct {
	name    string
	expires time.Time
	pending bool
}

// Resolver caches reverse DNS names for tailnet addresses.
type Resolver struct {
	mu         sync.Mutex
	entries    map[string]*entry
	maxEntries int
	// sweepAfter is the earliest expiry seen by the last sweep of a full
	// cache; sweeping again before it would find nothing to drop.
	sweepAfter time.Time
	// slots holds one token per lookup in flight.
	slots   chan struct{}
	lookup  func(ctx context.Context, ip string) ([]string, error)
	nowFunc func() time.Time
}

var defaultResolver = NewResolver(magicDNSLookup)

// Default returns the process-wide resolver backed by MagicDNS.
func Default() *Resolver { return defaultResolver }

// NewResolver builds a resolver around lookup, which returns PTR names for ip.
func NewResolver(lookup func(ctx context.Context, ip string) ([]string, error)) *Resolver {
	return newResolver(lookup, maxEntries, maxLookups)
}

func newResolver(lookup func(ctx context.Context, ip string) ([]string, error), entries, lookups int) *Resolver {
	return &Resolver{
		entries:    make(map[string]*entry),
		maxEntries: entries,
		slots:      make(chan struct{}, lookups),
		lookup:     lookup,
		nowFunc:    time.Now,
	}
}

func magicDNSLookup(ctx context.Context, ip string) ([]string, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, magicDNS)
		},
	}
	return resolver.LookupAddr(ctx, ip)
}

// Name returns the cached full DNS name for a tailnet ip, without the trailing
// dot, or "" when it is not known yet. When the cache has nothing fresh it
// starts a background lookup and returns whatever it had before.
func (r *Resolver) Name(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if r == nil || !IsTailnet(parsed) {
		return ""
	}
	key := parsed.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.entries[key]
	if current != nil && (current.pending || r.nowFunc().Before(current.expires)) {
		return current.name
	}
	if current == nil && !r.roomLocked() {
		return ""
	}
	select {
	case r.slots <- struct{}{}:
	default:
		// Too many lookups in flight: try again on a later call.
		if current == nil {
			return ""
		}
		return current.name
	}
	if current == nil {
		current = &entry{}
		r.entries[key] = current
	}
	current.pending = true
	go func() {
		defer func() { <-r.slots }()
		r.resolve(key)
	}()
	return current.name
}

func (r *Resolver) resolve(ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	names, errLookup := r.lookup(ctx, ip)
	name := ""
	if errLookup == nil {
		for _, candidate := range names {
			if candidate = strings.TrimSuffix(strings.TrimSpace(candidate), "."); candidate != "" {
				name = candidate
				break
			}
		}
	}
	ttl := successTTL
	if name == "" {
		ttl = failureTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.entries[ip]
	if current == nil {
		current = &entry{}
		r.entries[ip] = current
	}
	if name != "" {
		current.name = name
	}
	current.pending = false
	current.expires = r.nowFunc().Add(ttl)
}

// roomLocked reports whether the cache can take a new address, dropping
// expired entries first when it is full. The scan is bounded by maxEntries
// and skipped until the earliest entry it kept has expired.
func (r *Resolver) roomLocked() bool {
	if len(r.entries) < r.maxEntries {
		return true
	}
	now := r.nowFunc()
	if now.Before(r.sweepAfter) {
		return false
	}
	var next time.Time
	for key, current := range r.entries {
		switch {
		case current.pending:
		case now.After(current.expires):
			delete(r.entries, key)
		case next.IsZero() || current.expires.Before(next):
			next = current.expires
		}
	}
	r.sweepAfter = next
	return len(r.entries) < r.maxEntries
}

// MachineName is the short machine name for a client address: this host's
// name for loopback, the MagicDNS short name for a tailnet address, else "".
func MachineName(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() {
		return LocalHostName()
	}
	return ShortName(defaultResolver.Name(parsed.String()))
}

// LocalHostName is this host's short name, or "" when the OS does not say.
func LocalHostName() string {
	host, errHost := os.Hostname()
	if errHost != nil {
		return ""
	}
	return ShortName(host)
}

// SelfName is this host's full MagicDNS name, or "" until a lookup of one of
// its tailnet addresses has finished.
func SelfName() string {
	addrs, errAddrs := net.InterfaceAddrs()
	if errAddrs != nil {
		return ""
	}
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if !ok || !IsTailnet(network.IP) {
			continue
		}
		if name := defaultResolver.Name(network.IP.String()); name != "" {
			return name
		}
	}
	return ""
}

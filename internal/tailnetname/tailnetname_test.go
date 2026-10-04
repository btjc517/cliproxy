package tailnetname

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestShortName(t *testing.T) {
	for in, want := range map[string]string{
		"mbp-m3.tailf7303e.ts.net.": "mbp-m3",
		"desktop-home":              "desktop-home",
		"":                          "",
	} {
		if got := ShortName(in); got != want {
			t.Errorf("ShortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsTailnet(t *testing.T) {
	if !IsTailnet(net.ParseIP("100.101.102.103")) || IsTailnet(net.ParseIP("192.168.1.2")) || IsTailnet(nil) {
		t.Fatal("tailnet range check is wrong")
	}
}

func TestResolverCachesWithoutBlocking(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	calls := 0
	names := map[string][]string{"100.101.102.103": {"desktop-home.tailf7303e.ts.net."}}
	resolver := NewResolver(func(ctx context.Context, ip string) ([]string, error) {
		calls++
		<-release
		if found, ok := names[ip]; ok {
			return found, nil
		}
		return nil, errors.New("no PTR")
	})
	resolver.nowFunc = func() time.Time { return now }

	if got := resolver.Name("192.168.1.2"); got != "" {
		t.Fatalf("non-tailnet name = %q, want empty", got)
	}
	// Mark the lookup pending without running it, the way Name would before
	// its goroutine finishes, and check Name returns at once.
	resolver.entries["100.101.102.103"] = &entry{pending: true}
	if got := resolver.Name("100.101.102.103"); got != "" {
		t.Fatalf("pending name = %q, want empty", got)
	}
	close(release)
	resolver.resolve("100.101.102.103")
	if got := resolver.Name("100.101.102.103"); got != "desktop-home.tailf7303e.ts.net" {
		t.Fatalf("resolved name = %q", got)
	}
	resolver.resolve("100.64.0.9")
	if got := resolver.entries["100.64.0.9"]; got == nil || got.name != "" || !got.expires.Equal(now.Add(failureTTL)) {
		t.Fatalf("failed lookup entry = %+v, want an empty name cached for the failure TTL", got)
	}
	if calls != 2 {
		t.Fatalf("lookups = %d, want 2", calls)
	}
}

// waitIdle blocks until no lookup holds a slot.
func waitIdle(resolver *Resolver) {
	for i := 0; i < cap(resolver.slots); i++ {
		resolver.slots <- struct{}{}
	}
	for i := 0; i < cap(resolver.slots); i++ {
		<-resolver.slots
	}
}

func TestResolverBoundsLookupsInFlight(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int64
	resolver := newResolver(func(ctx context.Context, ip string) ([]string, error) {
		calls.Add(1)
		<-release
		return []string{"host-" + ip + ".tail.ts.net."}, nil
	}, 1024, 2)

	// A burst of new clients while DNS hangs.
	for i := 1; i <= 50; i++ {
		resolver.Name(fmt.Sprintf("100.64.0.%d", i))
	}
	resolver.mu.Lock()
	pending, entries := 0, len(resolver.entries)
	for _, current := range resolver.entries {
		if current.pending {
			pending++
		}
	}
	resolver.mu.Unlock()
	if pending > 2 || entries > 2 {
		t.Fatalf("%d lookups pending and %d entries during the burst, want at most 2", pending, entries)
	}

	close(release)
	waitIdle(resolver)
	if got := calls.Load(); got != 2 {
		t.Fatalf("lookups = %d, want 2", got)
	}
	// Freed slots let a later call start the next lookup.
	resolver.Name("100.64.0.9")
	waitIdle(resolver)
	if got := resolver.Name("100.64.0.9"); got != "host-100.64.0.9.tail.ts.net" {
		t.Fatalf("name after a slot freed = %q", got)
	}
}

func TestResolverCapsCacheEntries(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	resolver := newResolver(func(ctx context.Context, ip string) ([]string, error) {
		calls.Add(1)
		return []string{"host.tail.ts.net."}, nil
	}, 3, 8)
	resolver.nowFunc = func() time.Time { return now }

	for i := 1; i <= 3; i++ {
		resolver.Name(fmt.Sprintf("100.64.0.%d", i))
	}
	waitIdle(resolver)
	if got := resolver.Name("100.64.0.4"); got != "" || len(resolver.entries) != 3 || calls.Load() != 3 {
		t.Fatalf("full cache: name %q, %d entries, %d lookups, want no new entry or lookup", got, len(resolver.entries), calls.Load())
	}

	// Once the cached names expire there is room again.
	now = now.Add(successTTL + time.Minute)
	resolver.Name("100.64.0.4")
	waitIdle(resolver)
	if len(resolver.entries) != 1 || resolver.Name("100.64.0.4") != "host.tail.ts.net" {
		t.Fatalf("after expiry: %d entries, want only the new address", len(resolver.entries))
	}
}

// A full-cache sweep skips a pending lookup and waits for the earliest expiry
// it kept. When that lookup then fails, its short negative entry must free
// room when it expires, not when the hour-long entries do.
func TestResolverSweepsWhenAFailedLookupExpires(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC).UnixNano())
	release := make(chan struct{})
	resolver := newResolver(func(ctx context.Context, ip string) ([]string, error) {
		if ip == "100.64.0.3" {
			<-release
			return nil, errors.New("no PTR")
		}
		return []string{"host.tail.ts.net."}, nil
	}, 3, 8)
	resolver.nowFunc = func() time.Time { return time.Unix(0, clock.Load()).UTC() }

	resolver.Name("100.64.0.1")
	resolver.Name("100.64.0.2")
	waitIdle(resolver)
	// The third lookup hangs, so the cache is full with one entry pending.
	resolver.Name("100.64.0.3")
	if got := resolver.Name("100.64.0.4"); got != "" {
		t.Fatalf("full cache name = %q, want empty", got)
	}

	close(release)
	waitIdle(resolver)
	clock.Add(int64(failureTTL + time.Minute))
	resolver.Name("100.64.0.4")
	waitIdle(resolver)

	resolver.mu.Lock()
	_, added := resolver.entries["100.64.0.4"]
	_, kept := resolver.entries["100.64.0.3"]
	resolver.mu.Unlock()
	if !added || kept {
		t.Fatalf("after the failed entry expired: new address added %v, failed entry kept %v, want added and dropped", added, kept)
	}
}

func TestLocalHostNamePrefersTheFleetLabel(t *testing.T) {
	windows := func() (string, error) { return "DESKTOP-A1GNVPI", nil }
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"agent-cloud label", map[string]string{"AC_HOST_LABEL": "desktop-home"}, "desktop-home"},
		{"explicit name wins", map[string]string{"CLIPROXY_HOST_NAME": "pool", "AC_HOST_LABEL": "desktop-home"}, "pool"},
		{"blank label ignored", map[string]string{"AC_HOST_LABEL": "  "}, "DESKTOP-A1GNVPI"},
		{"OS hostname", map[string]string{}, "DESKTOP-A1GNVPI"},
	}
	for _, c := range cases {
		if got := localHostName(env(c.env), windows); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	failing := func() (string, error) { return "", errors.New("no hostname") }
	if got := localHostName(env(nil), failing); got != "" {
		t.Errorf("failing hostname: got %q, want empty", got)
	}
}

package tailnetname

import (
	"context"
	"errors"
	"net"
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

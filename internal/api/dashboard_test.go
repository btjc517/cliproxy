package api

import "testing"

func TestFromLoopbackOrTailnet(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5000":          true,
		"[::1]:5000":              true,
		"100.65.36.120:5000":      true,
		"[fd7a:115c:a1e0::1]:443": true,
		"172.28.96.1:5000":        false,
		"192.168.1.20:5000":       false,
		"100.128.0.1:5000":        false,
		"not-an-address":          false,
	}
	for addr, want := range cases {
		if got := fromLoopbackOrTailnet(addr); got != want {
			t.Errorf("fromLoopbackOrTailnet(%q) = %v, want %v", addr, got, want)
		}
	}
}

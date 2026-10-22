package config

import "testing"

func TestClaudeStatelessThreadsParsesFromEveryLayout(t *testing.T) {
	for name, raw := range map[string]string{
		"v8 upstream":      "config-version: 8\nupstream:\n  claude:\n    stateless-threads: true\n",
		"legacy":           "claude:\n  stateless-threads: true\n",
		"historical OAuth": "oauth:\n  providers:\n    claude:\n      stateless-threads: true\n",
	} {
		cfg, errParse := ParseConfigBytes([]byte(raw))
		if errParse != nil {
			t.Fatalf("%s: %v", name, errParse)
		}
		if !cfg.Claude.StatelessThreads {
			t.Fatalf("%s: stateless-threads was not read", name)
		}
	}
}

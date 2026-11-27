package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Trimming a padded name would point the save at a different file.
func TestFileTokenStoreRefusesNamesThatChangeWhenTrimmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth func(dir string) *cliproxyauth.Auth
	}{
		{"file name", func(string) *cliproxyauth.Auth {
			return &cliproxyauth.Auth{ID: " selected.json", FileName: " selected.json"}
		}},
		{"path", func(dir string) *cliproxyauth.Auth {
			return &cliproxyauth.Auth{ID: "selected.json", Attributes: map[string]string{"path": filepath.Join(dir, "selected.json") + " "}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sibling := filepath.Join(dir, "selected.json")
			original := []byte(`{"type":"claude","refresh_token":"old"}`)
			if err := os.WriteFile(sibling, original, 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			auth := tc.auth(dir)
			auth.Provider = "claude"
			auth.Metadata = map[string]any{"type": "claude", "refresh_token": "replacement"}
			if _, err := store.Save(context.Background(), auth); err == nil {
				t.Error("saved under a name that changes when trimmed")
			}
			if b, err := os.ReadFile(sibling); err != nil || string(b) != string(original) {
				t.Error("overwrote the sibling file")
			}
		})
	}
}

package store

import (
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Each store must refuse a name that would point at another file once trimmed.
func TestStoresRefuseNamesThatChangeWhenTrimmed(t *testing.T) {
	dir := t.TempDir()
	resolvers := map[string]func(*cliproxyauth.Auth) (string, error){
		"git":      (&GitTokenStore{baseDir: dir}).resolveAuthPath,
		"postgres": (&PostgresStore{authDir: dir}).resolveAuthPath,
		"object":   (&ObjectTokenStore{authDir: dir}).resolveAuthPath,
	}
	auths := map[string]*cliproxyauth.Auth{
		"file name": {ID: " selected.json", FileName: " selected.json"},
		"path":      {ID: "selected.json", Attributes: map[string]string{"path": filepath.Join(dir, "selected.json") + " "}},
	}
	for storeName, resolve := range resolvers {
		for authName, auth := range auths {
			if path, err := resolve(auth); err == nil {
				t.Errorf("%s store resolved padded %s to %q", storeName, authName, path)
			}
		}
		if path, err := resolve(&cliproxyauth.Auth{ID: "selected.json", FileName: "selected.json"}); err != nil || path != filepath.Join(dir, "selected.json") {
			t.Errorf("%s store resolved a plain name to %q, %v", storeName, path, err)
		}
	}
}

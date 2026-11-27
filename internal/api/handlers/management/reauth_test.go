package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestTargetedReauthentication(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			target := &coreauth.Auth{ID: "original.json", FileName: "original.json", Provider: provider, Metadata: map[string]any{"email": "User@example.test", "refresh_token": "fixture"}}
			scope := "organization_uuid"
			if provider == "codex" {
				scope = "account_id"
			} else {
				target.Metadata["account_uuid"] = "user"
			}
			target.Metadata[scope] = "original"
			if _, err := manager.Register(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
			record := &coreauth.Auth{ID: "new.json", FileName: "new.json", Provider: provider, Metadata: map[string]any{"email": "user@example.test", scope: "other", "account_uuid": "user"}}
			if err := h.bindReauthRecord(target.ID, record); err == nil {
				t.Fatal("accepted another workspace")
			}
			record.Metadata[scope] = "original"
			record.Metadata["email"] = "other@example.test"
			if err := h.bindReauthRecord(target.ID, record); err == nil {
				t.Fatal("accepted another account")
			}
			record.Metadata["email"] = "user@example.test"
			if err := h.bindReauthRecord("missing.json", record); err == nil {
				t.Fatal("accepted missing account")
			}
			if err := h.bindReauthRecord(target.ID, record); err != nil {
				t.Fatal(err)
			}
			if record.ID != target.ID || record.FileName != target.FileName || record.Attributes["path"] != filepath.Join(dir, target.FileName) {
				t.Fatal("did not retain original row")
			}
		})
	}
}

// workspaceCodexOAuthService returns a workspace named after the code.
type workspaceCodexOAuthService struct{ fakeCodexOAuthService }

func (f *workspaceCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkce *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	bundle, err := f.fakeCodexOAuthService.ExchangeCodeForTokens(ctx, code, pkce)
	if bundle != nil {
		bundle.TokenData.AccountID = "workspace-" + code
	}
	return bundle, err
}

func TestReauthCallbackUpdatesOnlySelectedRow(t *testing.T) {
	oldService := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { return &workspaceCodexOAuthService{} }
	t.Cleanup(func() { newCodexOAuthService = oldService })
	dir := t.TempDir()
	// The fixture provider uses the code to choose the account it returns.
	name := "codex-codex-owner@example.test-free.json"
	metadata := map[string]any{"type": "codex", "email": "codex-owner@example.test", "account_id": "workspace-owner", "refresh_token": "original", "priority": -1}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := coreauth.NewManager(nil, nil, nil)
	if _, err = m.Register(context.Background(), &coreauth.Auth{ID: name, FileName: name, Provider: "codex", Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	for _, code := range []string{"someone-else", "owner"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=codex&reauth_id="+name, nil))
		var login struct{ State string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.State == "" {
			t.Fatal("sign-in did not start", w.Code)
		}
		if _, err = WriteOAuthCallbackFileForPendingSession(dir, "codex", login.State, code, ""); err != nil {
			t.Fatal(err)
		}
		waitForOAuthSessionDone(t, login.State)
		_, message, _, _, done, ok := GetOAuthSessionDetails(login.State)
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if code != "owner" {
			if !ok || done || !strings.Contains(message, "different account") || string(saved) != string(data) {
				t.Fatal("wrong account was accepted or changed the selected file")
			}
		} else {
			var updated map[string]any
			if !done || json.Unmarshal(saved, &updated) != nil || updated["refresh_token"] != "refresh-owner" || updated["priority"] != float64(-1) {
				t.Fatal("expected refreshed credentials on the original reserve row")
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("sign-in created another row", files)
	}
}

func TestReauthKeepsOtherClaudeFiles(t *testing.T) {
	dir := t.TempDir()
	canonical := claude.CredentialFileName("user@example.test", "org", "account")
	sibling := filepath.Join(dir, canonical)
	legacy := filepath.Join(dir, "claude-user@example.test.json")
	selected := filepath.Join(dir, "saved", canonical)
	if err := os.Mkdir(filepath.Dir(selected), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"type":"claude","email":"user@example.test","organization_uuid":"org","account_uuid":"account","refresh_token":"old","priority":-1}`)
	for _, p := range []string{sibling, legacy, selected} {
		if err := os.WriteFile(p, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := coreauth.NewManager(nil, nil, nil)
	target := &coreauth.Auth{ID: "saved/" + canonical, FileName: canonical, Provider: "claude", Attributes: map[string]string{"path": selected}}
	if err := json.Unmarshal(original, &target.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
	record := &coreauth.Auth{ID: canonical, FileName: canonical, Provider: "claude", Metadata: map[string]any{"email": "user@example.test", "organization_uuid": "org", "account_uuid": "account", "refresh_token": "replacement", "type": "claude"}}
	if err := h.bindReauthRecord(target.ID, record); err != nil {
		t.Fatal(err)
	}
	var runtimeID string
	h.SetPostAuthPersistHook(func(_ context.Context, a *coreauth.Auth) error { runtimeID = a.ID; return nil })
	saved, err := h.persistTokenRecord(context.Background(), record, true)
	if runtimeID != target.ID {
		t.Fatalf("runtime updated %q, want %q", runtimeID, target.ID)
	}
	if err != nil || saved != selected {
		t.Fatalf("saved %q, err %v", saved, err)
	}
	for _, p := range []string{sibling, legacy} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != string(original) {
			t.Fatal("changed sibling credential", p)
		}
	}
	b, err := os.ReadFile(selected)
	if err != nil || !strings.Contains(string(b), "replacement") {
		t.Fatal("selected row was not updated")
	}
}

// A legacy file named only by email must not be replaced when the provider
// cannot confirm the workspace.
func TestReauthCallbackRefusesUnconfirmedWorkspace(t *testing.T) {
	oldService := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { return &fakeCodexOAuthService{} }
	t.Cleanup(func() { newCodexOAuthService = oldService })
	dir := t.TempDir()
	name := "codex-codex-owner@example.test-free.json"
	data := []byte(`{"type":"codex","email":"codex-owner@example.test","refresh_token":"original"}`)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := coreauth.NewManager(nil, nil, nil)
	target := &coreauth.Auth{ID: name, FileName: name, Provider: "codex"}
	if err := json.Unmarshal(data, &target.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=codex&reauth_id="+name, nil))
	var login struct{ State string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || login.State == "" {
		t.Fatal("sign-in did not start", w.Code)
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(dir, "codex", login.State, "owner", ""); err != nil {
		t.Fatal(err)
	}
	waitForOAuthSessionDone(t, login.State)
	_, message, _, _, done, ok := GetOAuthSessionDetails(login.State)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || done || !strings.Contains(message, "could not confirm the original workspace") || string(saved) != string(data) {
		t.Fatalf("replaced a file without a confirmed workspace: done=%v message=%q", done, message)
	}
}

func TestReauthNeedsAVerifiedWorkspace(t *testing.T) {
	const email = "user@example.test"
	hash := func(id string) string {
		digest := sha256.Sum256([]byte(id))
		return hex.EncodeToString(digest[:])[:8]
	}
	cases := []struct {
		name, provider, file string
		stored, returned     map[string]any
		accept               bool
	}{
		{"claude email-only file and reply", "claude", claude.CredentialFileName(email, "", ""), nil, nil, false},
		{"claude account hash, no workspace", "claude", claude.CredentialFileName(email, "", "account"), map[string]any{"account_uuid": "account"}, map[string]any{"account_uuid": "account"}, false},
		{"claude email-only file, reply with workspace", "claude", claude.CredentialFileName(email, "", ""), nil, map[string]any{"organization_uuid": "org", "account_uuid": "account"}, false},
		{"claude file named for the verified workspace", "claude", claude.CredentialFileName(email, "org", ""), map[string]any{"account_uuid": "account"}, map[string]any{"organization_uuid": "org", "account_uuid": "account"}, true},
		{"codex email-only file and reply", "codex", codex.CredentialFileName(email, "plus", "", true), nil, map[string]any{"account_id": ""}, false},
		{"codex file named for another workspace", "codex", codex.CredentialFileName(email, "plus", hash("other"), true), nil, map[string]any{"account_id": "workspace"}, false},
		{"codex file named for the verified workspace", "codex", codex.CredentialFileName(email, "plus", hash("workspace"), true), nil, map[string]any{"account_id": "workspace"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			target := &coreauth.Auth{ID: tc.file, FileName: tc.file, Provider: tc.provider, Metadata: map[string]any{"email": email, "refresh_token": "fixture"}}
			for k, v := range tc.stored {
				target.Metadata[k] = v
			}
			if _, err := m.Register(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)
			// Name the reply the way the sign-in flow does.
			org, _ := tc.returned["organization_uuid"].(string)
			account, _ := tc.returned["account_uuid"].(string)
			workspace, _ := tc.returned["account_id"].(string)
			fileName := claude.CredentialFileName(email, org, account)
			if tc.provider == "codex" {
				workspaceHash := ""
				if workspace != "" {
					workspaceHash = hash(workspace)
				}
				fileName = codex.CredentialFileName(email, "plus", workspaceHash, true)
			}
			record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: tc.provider, Metadata: map[string]any{"email": email}}
			for k, v := range tc.returned {
				record.Metadata[k] = v
			}
			err := h.bindReauthRecord(target.ID, record)
			if tc.accept && err != nil {
				t.Fatalf("refused a confirmed workspace: %v", err)
			}
			if !tc.accept && err == nil {
				t.Fatal("accepted a sign-in without a verified workspace")
			}
		})
	}
}

// A name that changes when trimmed points at a different file. Sign-in must
// refuse it and leave both files alone.
func TestReauthWritesOnlyTheExactSelectedFile(t *testing.T) {
	for _, withPath := range []bool{true, false} {
		name := "file name only"
		if withPath {
			name = "path attribute"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			original := []byte(`{"type":"claude","email":"user@example.test","organization_uuid":"org","account_uuid":"account","refresh_token":"old"}`)
			padded := " selected.json"
			sibling := filepath.Join(dir, "selected.json")
			selected := filepath.Join(dir, padded)
			for _, p := range []string{sibling, selected} {
				if err := os.WriteFile(p, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			m := coreauth.NewManager(nil, nil, nil)
			target := &coreauth.Auth{ID: padded, FileName: padded, Provider: "claude"}
			if withPath {
				target.Attributes = map[string]string{"path": selected}
			}
			if err := json.Unmarshal(original, &target.Metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Register(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
			fileName := claude.CredentialFileName("user@example.test", "org", "account")
			record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: "claude", Metadata: map[string]any{"type": "claude", "email": "user@example.test", "organization_uuid": "org", "account_uuid": "account", "refresh_token": "replacement"}}
			if err := h.bindReauthRecord(target.ID, record); err == nil {
				_, _ = h.persistTokenRecord(context.Background(), record, true)
				t.Error("accepted a selected file whose name changes when trimmed")
			}
			for _, p := range []string{sibling, selected} {
				if b, err := os.ReadFile(p); err != nil || string(b) != string(original) {
					t.Errorf("changed %q", filepath.Base(p))
				}
			}
		})
	}
}

// Sign-in again saves only to the path bound to the selected row.
func TestReauthSaveNeedsTheBoundPath(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, coreauth.NewManager(nil, nil, nil))
	record := &coreauth.Auth{ID: "unbound.json", FileName: "unbound.json", Provider: "claude", Metadata: map[string]any{"type": "claude", "email": "user@example.test"}}
	if _, err := h.persistTokenRecord(context.Background(), record, true); err == nil {
		t.Fatal("saved a sign-in that was not bound to a selected file")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatal("wrote a file without a selected path", files)
	}
}

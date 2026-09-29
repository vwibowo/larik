package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"larik/internal/config"
)

// fakeOAuthServer is an MCP server that requires a bearer token, and the
// authorization server that hands one out: metadata discovery, dynamic
// client registration, and a token endpoint.
func fakeOAuthServer(t *testing.T, sse bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var tokens atomic.Int32
	srv := sdk.NewServer(&sdk.Implementation{Name: "secured", Version: "1"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "whoami", Description: "who"}, func(ctx context.Context, req *sdk.CallToolRequest, in struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "you"}}}, nil, nil
	})
	var mcpHandler http.Handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	if sse {
		mcpHandler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	}
	var ts *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+ts.URL+`/.well-known/oauth-protected-resource"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": ts.URL + "/mcp", "authorization_servers": []string{ts.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": ts.URL, "authorization_endpoint": ts.URL + "/authorize", "token_endpoint": ts.URL + "/token",
			"registration_endpoint": ts.URL + "/register", "response_types_supported": []string{"code"},
			"grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var meta map[string]any
		json.NewDecoder(r.Body).Decode(&meta)
		meta["client_id"] = "larik-client"
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, meta)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "r1" {
			tokens.Add(1)
			writeJSON(w, map[string]any{"access_token": "good-token", "token_type": "Bearer", "refresh_token": "r1", "expires_in": 7200})
			return
		}
		if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" || r.Form.Get("client_id") != "larik-client" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		tokens.Add(1)
		writeJSON(w, map[string]any{"access_token": "good-token", "token_type": "Bearer", "refresh_token": "r1", "expires_in": 3600})
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &tokens
}

// fakeBrowser plays the user approving the sign-in: it follows the
// authorization URL straight back to the redirect URI with a code.
func fakeBrowser(t *testing.T) func(string) {
	return func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "larik-client" {
			t.Errorf("authorization URL: %s", authURL)
		}
		go http.Get(q.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
	}
}

func waitState(t *testing.T, m *Manager, want State) Status {
	t.Helper()
	m.Tools(context.Background())
	for _, st := range m.Statuses() {
		if st.Name == "secured" {
			if st.State != want {
				t.Fatalf("state = %s (%s), want %s", st.State, st.Err, want)
			}
			return st
		}
	}
	t.Fatal("no status")
	return Status{}
}

func TestOAuthSignIn(t *testing.T) {
	testOAuthSignIn(t, "http")
}

// sse servers get the same sign-in through the HTTP client, since the
// SDK's SSE transport has no OAuth handler of its own.
func TestOAuthSignInSSE(t *testing.T) {
	testOAuthSignIn(t, "sse")
}

func testOAuthSignIn(t *testing.T, transport string) {
	ts, tokens := fakeOAuthServer(t, transport == "sse")
	cfg := newCfg(t, map[string]config.MCPServer{"secured": {Type: transport, URL: ts.URL + "/mcp", Trusted: true}})
	cfg.ConfigDir = t.TempDir()

	m := NewManager(cfg, "test")
	opened := 0
	m.OpenURL = func(string) { opened++ }
	m.Start()
	defer m.Close()
	st := waitState(t, m, StateNeedsAuth)
	if opened != 0 || !strings.Contains(st.Err, "/mcp login secured") {
		t.Fatalf("a background connect must not open a browser (%d) and should say how to sign in: %q", opened, st.Err)
	}

	var shown string
	m.SetSignInHook(func(_, u string) { shown = u })
	m.OpenURL = fakeBrowser(t)
	done, err := m.Login("secured")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("sign-in didn't finish")
	}
	if st := waitState(t, m, StateConnected); len(st.Tools) != 1 || shown == "" || tokens.Load() != 1 {
		t.Fatalf("after sign-in: %+v, url shown %v, tokens %d", st, shown != "", tokens.Load())
	}
	path := m.authPath(cfg.MCPServers["secured"])
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the sign-in should be saved privately: %v %v", fi, err)
	}

	// A new session uses the saved sign-in without a browser.
	again := NewManager(cfg, "test")
	again.OpenURL = func(string) { t.Error("no browser expected") }
	again.Start()
	defer again.Close()
	waitState(t, again, StateConnected)
	if tokens.Load() != 1 {
		t.Fatalf("the saved token should be reused, got %d token requests", tokens.Load())
	}

	// An expired token is refreshed without a browser, and the new one saved.
	stored, _ := loadAuth(path)
	stored.Token.AccessToken, stored.Token.Expiry = "stale", time.Now().Add(-time.Hour)
	saveAuth(path, stored)
	refreshed := NewManager(cfg, "test")
	refreshed.OpenURL = func(string) { t.Error("no browser expected for a refresh") }
	refreshed.Start()
	defer refreshed.Close()
	waitState(t, refreshed, StateConnected)
	if saved, _ := loadAuth(path); saved.Token.AccessToken != "good-token" || time.Until(saved.Token.Expiry) < time.Hour {
		t.Fatalf("the refreshed token should be saved: %+v", saved.Token)
	}

	if err := again.Logout("secured"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(again.Statuses()[0].Err, "signed out") {
		t.Fatal("logout should remove the saved sign-in")
	}
	if _, err := again.Login("nope"); err == nil {
		t.Fatal("unknown server")
	}
}

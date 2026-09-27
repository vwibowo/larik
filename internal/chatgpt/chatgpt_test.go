package chatgpt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// jwt builds an unsigned token with the given claims.
func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// fakeIssuer is an OAuth server that hands out tokens for any code and
// counts refreshes.
func fakeIssuer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/oauth/token" || r.Form.Get("client_id") != ClientID {
			http.Error(w, `{"error":"invalid_request"}`, 400)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			refreshes.Add(1)
		} else if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant","error_description":"bad code"}`, 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_1"}}),
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "rt_2",
		})
	}))
	t.Cleanup(srv.Close)
	old := Issuer
	Issuer = srv.URL
	t.Cleanup(func() { Issuer = old })
	return srv, &refreshes
}

// browser plays the user: it follows the sign-in URL's redirect with a
// code (or tampered state).
func browser(t *testing.T, tamper bool) func(string) {
	return func(signIn string) {
		u, _ := url.Parse(signIn)
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != ClientID || !strings.Contains(q.Get("scope"), "offline_access") {
			t.Errorf("sign-in URL: %s", signIn)
		}
		state := q.Get("state")
		if tamper {
			state = "other"
		}
		go http.Get(q.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(state))
	}
}

func TestLogin(t *testing.T) {
	fakeIssuer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := Login(ctx, browser(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccountID != "acct_1" || tok.Email != "me@example.com" || tok.RefreshToken != "rt_2" || time.Until(tok.Expires) < 50*time.Minute {
		t.Fatalf("tokens: %+v", tok)
	}

	if _, err := Login(ctx, browser(t, true)); err == nil || !strings.Contains(err.Error(), "didn't match") {
		t.Fatalf("a reply with the wrong state must be rejected, got %v", err)
	}
}

func TestSourceRefreshesAndSaves(t *testing.T) {
	_, refreshes := fakeIssuer(t)
	path := filepath.Join(t.TempDir(), "chatgpt-auth.json")
	if _, _, err := NewSource(path).Token(context.Background()); err == nil || !strings.Contains(err.Error(), "/connect codex") {
		t.Fatalf("no sign-in should say how to sign in, got %v", err)
	}
	Save(path, Tokens{AccessToken: "old", RefreshToken: "rt_1", AccountID: "acct_1", Expires: time.Now().Add(time.Minute)})
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("sign-in file must be private, got %v", fi.Mode().Perm())
	}

	s := NewSource(path)
	tok, account, err := s.Token(context.Background())
	if err != nil || tok == "old" || account != "acct_1" || refreshes.Load() != 1 {
		t.Fatalf("an expiring token should refresh: tok %q account %q err %v refreshes %d", tok, account, err, refreshes.Load())
	}
	saved, _ := Load(path)
	if saved.AccessToken != tok || saved.RefreshToken != "rt_2" {
		t.Fatalf("refreshed tokens should be saved: %+v", saved)
	}
	s.Token(context.Background())
	if refreshes.Load() != 1 {
		t.Fatal("a fresh token must not refresh again")
	}
}

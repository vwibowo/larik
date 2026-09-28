// Package chatgpt signs in to a ChatGPT account with the OAuth flow Codex
// uses, so Larik can run Codex models on the user's ChatGPT plan instead
// of an API key.
//
// Login opens the sign-in page in the browser, receives the result on a
// local callback and exchanges it for tokens (PKCE, no client secret).
// Tokens are Larik's own, stored readable only by the user and refreshed
// before they expire; nothing is shared with the Codex CLI.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"larik/internal/filelock"
)

// The public OAuth client Codex uses; its redirect is registered for this
// local callback.
const (
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	CallbackPort = 1455
	callbackPath = "/auth/callback"
	scope        = "openid profile email offline_access"

	// BaseURL serves the Responses API for ChatGPT sign-ins.
	BaseURL = "https://chatgpt.com/backend-api/codex"
)

// Issuer is the OAuth server; tests point it elsewhere.
var Issuer = "https://auth.openai.com"

// Tokens is a signed-in session.
type Tokens struct {
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccountID    string    `json:"account_id"`
	Email        string    `json:"email,omitempty"`
	Expires      time.Time `json:"expires"`
}

// Path is where Larik keeps its ChatGPT sign-in, under configDir.
func Path(configDir string) string { return filepath.Join(configDir, "chatgpt-auth.json") }

// Load reads a saved sign-in; os.ErrNotExist means not signed in.
func Load(path string) (Tokens, error) {
	var t Tokens
	data, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Save writes tokens readable only by the user.
func Save(path string, t Tokens) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SignedIn reports whether a sign-in is saved at path.
func SignedIn(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Login runs the browser sign-in. open is called with the sign-in URL
// (open it in a browser, and show it in case that fails); it returns when
// the browser comes back or ctx ends.
func Login(ctx context.Context, open func(url string)) (Tokens, error) {
	verifier := randomString(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(32)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", CallbackPort))
	if err != nil {
		return Tokens{}, fmt.Errorf("can't listen on port %d for the sign-in callback (is another sign-in running?): %w", CallbackPort, err)
	}
	redirect := fmt.Sprintf("http://localhost:%d%s", CallbackPort, callbackPath)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("the sign-in reply didn't match this request; try again")
		case q.Get("error") != "":
			res.err = fmt.Errorf("sign-in failed: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("the sign-in reply had no code")
		default:
			res.code = q.Get("code")
		}
		page(w, res.err)
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {redirect},
		"scope":                      {scope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
	}
	open(Issuer + "/oauth/authorize?" + q.Encode())

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return Tokens{}, ctx.Err()
	}
	if res.err != nil {
		return Tokens{}, res.err
	}
	t, err := exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	})
	if err == nil && t.AccountID == "" {
		return Tokens{}, errors.New("ChatGPT sign-in: the account has no ChatGPT workspace (is it on a ChatGPT plan?)")
	}
	return t, err
}

// exchange posts to the token endpoint and reads the account from the
// ID token.
func exchange(ctx context.Context, form url.Values) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	var body struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(data, &body)
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		msg := body.Description
		if msg == "" {
			msg = body.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return Tokens{}, fmt.Errorf("ChatGPT sign-in: %s", msg)
	}
	t := Tokens{IDToken: body.IDToken, AccessToken: body.AccessToken, RefreshToken: body.RefreshToken}
	t.AccountID, t.Email = accountOf(body.IDToken)
	t.Expires = expiryOf(body.AccessToken)
	if t.Expires.IsZero() && body.ExpiresIn > 0 {
		t.Expires = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return t, nil
}

// Refresh gets a new access token with the refresh token.
func Refresh(ctx context.Context, t Tokens) (Tokens, error) {
	n, err := exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {ClientID},
		"scope":         {"openid profile email"},
	})
	if err != nil {
		return Tokens{}, fmt.Errorf("%w; sign in again with /connect codex", err)
	}
	if n.RefreshToken == "" { // not every refresh rotates it
		n.RefreshToken = t.RefreshToken
	}
	if n.IDToken == "" || n.AccountID == "" { // not every refresh sends one
		n.IDToken, n.AccountID, n.Email = t.IDToken, t.AccountID, t.Email
	}
	return n, nil
}

// Source hands out a valid access token, refreshing and saving the
// sign-in at path when it is about to expire.
type Source struct {
	path string
	mu   sync.Mutex
	t    *Tokens
}

func NewSource(path string) *Source { return &Source{path: path} }

// Token returns the access token and ChatGPT account id.
func (s *Source) Token(ctx context.Context) (access, account string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.t == nil {
		t, err := Load(s.path)
		if errors.Is(err, os.ErrNotExist) {
			return "", "", errors.New("not signed in to ChatGPT; run /connect codex")
		}
		if err != nil {
			return "", "", err
		}
		s.t = &t
	}
	if time.Until(s.t.Expires) < refreshMargin {
		n, err := s.refresh(ctx)
		if err != nil {
			return "", "", err
		}
		s.t = &n
	}
	return s.t.AccessToken, s.t.AccountID, nil
}

// refreshMargin is how long before expiry a token is renewed.
const refreshMargin = 5 * time.Minute

// refresh renews the sign-in. Several sources share the file (subagents,
// fallback chains, other larik processes) and the issuer rotates the
// refresh token, so this holds a lock on the file and re-reads it first:
// when another source has already refreshed, its tokens are used rather
// than spending a refresh token that is no longer valid.
func (s *Source) refresh(ctx context.Context) (Tokens, error) {
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Tokens{}, err
	}
	defer lock.Close()
	if err := filelock.Lock(lock, false); err != nil {
		return Tokens{}, err
	}
	defer filelock.Unlock(lock)
	cur, err := Load(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Tokens{}, errors.New("not signed in to ChatGPT; run /connect codex")
	}
	if err != nil {
		return Tokens{}, err
	}
	if time.Until(cur.Expires) >= refreshMargin {
		return cur, nil // refreshed elsewhere
	}
	n, err := Refresh(ctx, cur)
	if err != nil {
		return Tokens{}, err
	}
	if err := Save(s.path, n); err != nil {
		return Tokens{}, err
	}
	return n, nil
}

// claims decodes a JWT's payload without verifying it; the tokens come
// straight from the issuer over TLS and are only read for display and
// routing.
func claims(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var c map[string]any
	_ = json.Unmarshal(data, &c)
	return c
}

func accountOf(idToken string) (account, email string) {
	c := claims(idToken)
	email, _ = c["email"].(string)
	if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
		account, _ = auth["chatgpt_account_id"].(string)
	}
	return account, email
}

func expiryOf(accessToken string) time.Time {
	if exp, ok := claims(accessToken)["exp"].(float64); ok {
		return time.Unix(int64(exp), 0)
	}
	return time.Time{}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func page(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := "Signed in to ChatGPT. You can close this tab and go back to larik."
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		msg = "Sign-in didn't complete: " + html.EscapeString(err.Error())
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>larik</title><body style="font:16px system-ui;margin:4em auto;max-width:32em">%s</body>`, msg)
}

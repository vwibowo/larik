package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"larik/internal/config"
)

// OAuth for http and sse servers. The SDK runs the flow (discovery, client
// registration, PKCE, token exchange and refresh); Larik supplies the
// browser step and keeps the tokens.
//
// A server connecting in the background never opens a browser: it fails
// with StateNeedsAuth, and /mcp login runs the sign-in on purpose.

// StateNeedsAuth is a server that asked for a sign-in nobody has done.
const StateNeedsAuth State = "needs sign-in"

var errNeedsLogin = errors.New("the server asks you to sign in")

// loginTimeout bounds how long a sign-in waits for the browser.
const loginTimeout = 5 * time.Minute

// storedAuth is a server's sign-in, kept in a private file so it lasts
// across sessions.
type storedAuth struct {
	URL          string        `json:"url"`
	ClientID     string        `json:"client_id"`
	ClientSecret string        `json:"client_secret,omitempty"`
	AuthURL      string        `json:"auth_url"`
	TokenURL     string        `json:"token_url"`
	AuthStyle    int           `json:"auth_style,omitempty"`
	RedirectURL  string        `json:"redirect_url"`
	Scopes       []string      `json:"scopes,omitempty"`
	Token        *oauth2.Token `json:"token"`
}

func (a *storedAuth) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: a.ClientID, ClientSecret: a.ClientSecret, RedirectURL: a.RedirectURL, Scopes: a.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: a.AuthURL, TokenURL: a.TokenURL, AuthStyle: oauth2.AuthStyle(a.AuthStyle)},
	}
}

// authPath is where a server's sign-in is kept: per server name and URL,
// so pointing a server at another URL starts over.
func (m *Manager) authPath(cfg config.MCPServer) string {
	sum := sha256.Sum256([]byte(config.ExpandEnv(cfg.URL)))
	return filepath.Join(m.cfg.ConfigDir, "mcp-auth", sanitize(cfg.Name)+"-"+hex.EncodeToString(sum[:4])+".json")
}

func loadAuth(path string) (*storedAuth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a storedAuth
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

var saveMu sync.Mutex

func saveAuth(path string, a *storedAuth) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SignedIn reports whether a server has a saved sign-in.
func (m *Manager) SignedIn(name string) bool {
	cfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return false
	}
	_, err := os.Stat(m.authPath(cfg))
	return err == nil
}

// savingSource saves a token whenever the underlying source refreshes it.
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
	save func(*oauth2.Token)
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := t.AccessToken != s.last
	s.last = t.AccessToken
	s.mu.Unlock()
	if changed {
		s.save(t)
	}
	return t, nil
}

// oauthHandler is the OAuth handler for an http or sse server. interactive runs
// the browser sign-in when the server asks for one; otherwise that ask
// fails with errNeedsLogin.
func (m *Manager) oauthHandler(s *server, interactive bool) (auth.OAuthHandler, error) {
	cfg := s.cfg
	path := m.authPath(cfg)
	stored, _ := loadAuth(path)
	oc := cfg.OAuth
	if oc == nil {
		oc = &config.MCPOAuth{}
	}

	redirect := ""
	switch {
	case oc.CallbackPort > 0:
		redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", oc.CallbackPort)
	case stored != nil && stored.RedirectURL != "":
		redirect = stored.RedirectURL
	default:
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}

	persist := func(conf *oauth2.Config, tok *oauth2.Token) {
		_ = saveAuth(path, &storedAuth{
			URL: config.ExpandEnv(cfg.URL), ClientID: conf.ClientID, ClientSecret: conf.ClientSecret,
			AuthURL: conf.Endpoint.AuthURL, TokenURL: conf.Endpoint.TokenURL, AuthStyle: int(conf.Endpoint.AuthStyle),
			RedirectURL: conf.RedirectURL, Scopes: conf.Scopes, Token: tok,
		})
	}
	hc := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         redirect,
		RequestRefreshToken: true,
		// The SDK accepts only localhost/127.0.0.1 redirects without TLS.
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if !interactive {
				return nil, errNeedsLogin
			}
			return m.browserSignIn(ctx, cfg.Name, redirect, args.URL)
		},
		NewTokenSource: func(ctx context.Context, conf *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			persist(conf, tok)
			return &savingSource{src: conf.TokenSource(ctx, tok), last: tok.AccessToken, save: func(t *oauth2.Token) { persist(conf, t) }}, nil
		},
	}
	if oc.ClientID != "" {
		hc.PreregisteredClient = &oauthex.ClientCredentials{ClientID: oc.ClientID}
		if secret := config.ExpandEnv(oc.ClientSecret); secret != "" {
			hc.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: secret}
		}
	} else {
		hc.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              "Larik",
			RedirectURIs:            []string{redirect},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}}
	}
	if len(oc.Scopes) > 0 {
		scopes := oc.Scopes
		hc.ScopeFilter = func([]string) []string { return scopes }
	}
	if stored != nil && stored.Token != nil && stored.URL == config.ExpandEnv(cfg.URL) {
		conf := stored.config()
		hc.InitialTokenSource = &savingSource{
			src: conf.TokenSource(context.Background(), stored.Token), last: stored.Token.AccessToken,
			save: func(t *oauth2.Token) { persist(conf, t) },
		}
	}
	return auth.NewAuthorizationCodeHandler(hc)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// browserSignIn opens authURL and waits for the authorization server to
// redirect back to redirect with a code.
func (m *Manager) browserSignIn(ctx context.Context, name, redirect, authURL string) (*auth.AuthorizationResult, error) {
	u, err := url.Parse(redirect)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("can't receive the sign-in on %s: %w", u.Host, err)
	}
	results := make(chan *auth.AuthorizationResult, 1)
	failures := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			fmt.Fprintf(w, signInPage, "Sign-in failed: "+html.EscapeString(msg))
			select {
			case failures <- fmt.Errorf("sign-in failed: %s", msg):
			default:
			}
			return
		}
		fmt.Fprintf(w, signInPage, "Signed in to "+html.EscapeString(name)+". You can close this tab and go back to larik.")
		select {
		case results <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	m.mu.Lock()
	hook := m.onSignIn
	m.mu.Unlock()
	if hook != nil {
		hook(name, authURL)
	}
	if m.OpenURL != nil {
		m.OpenURL(authURL)
	}
	timer := time.NewTimer(loginTimeout)
	defer timer.Stop()
	select {
	case res := <-results:
		return res, nil
	case err := <-failures:
		return nil, err
	case <-timer.C:
		return nil, errors.New("sign-in timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const signInPage = `<!doctype html><meta charset="utf-8"><title>larik</title><body style="font:16px system-ui;margin:4em auto;max-width:32em">%s</body>`

// SetSignInHook sets the function told each sign-in page's URL.
func (m *Manager) SetSignInHook(fn func(server, url string)) {
	m.mu.Lock()
	m.onSignIn = fn
	m.mu.Unlock()
}

// Login connects to an http or sse server again, running the browser sign-in if
// it asks for one. The returned channel closes when that attempt ends;
// Statuses then says how it went.
func (m *Manager) Login(name string) (<-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.cfg.MCPServers[name]
	switch {
	case !ok:
		return nil, fmt.Errorf("no MCP server named %q", name)
	case cfg.Transport() != "http" && cfg.Transport() != "sse":
		return nil, fmt.Errorf("%s uses %s; sign-in is for http and sse servers", name, cfg.Transport())
	case cfg.Disabled:
		return nil, fmt.Errorf("%s is disabled", name)
	case !m.cfg.Approved(cfg):
		return nil, fmt.Errorf("%s isn't approved yet; run /mcp approve %s first", name, name)
	case m.closed:
		return nil, errors.New("closed")
	}
	if old, ok := m.servers[name]; ok {
		select {
		case <-old.done:
		default:
			return nil, fmt.Errorf("%s is still connecting", name)
		}
		old.release()
	}
	s := &server{cfg: cfg, done: make(chan struct{}), state: StateConnecting, interactive: true}
	m.servers[name] = s
	go m.connect(s)
	return s.done, nil
}

// Logout forgets a server's sign-in and disconnects it.
func (m *Manager) Logout(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	err := os.Remove(m.authPath(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("not signed in to %s", name)
	}
	if err != nil {
		return err
	}
	if s, ok := m.servers[name]; ok {
		select {
		case <-s.done:
			s.release()
			s.tools, s.prompts, s.resources = nil, nil, false
			s.state, s.err = StateNeedsAuth, fmt.Errorf("signed out; sign in with /mcp login %s", name)
		default: // still connecting; it will fail or succeed on its own
		}
	}
	return nil
}

// oauthTransport gives an sse server's requests what the SDK's streamable
// transport does for http servers: the bearer token on each request, and
// on 401 or 403 the handler's sign-in, after which the request is sent
// once more.
type oauthTransport struct {
	handler auth.OAuthHandler
	base    http.RoundTripper
}

func (t oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte // kept to send again after a sign-in; messages are small
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	send := func() (*http.Response, error) {
		req := r.Clone(r.Context())
		if r.Body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
		}
		ts, err := t.handler.TokenSource(r.Context())
		if err != nil {
			return nil, err
		}
		if ts != nil {
			tok, err := ts.Token()
			if err != nil {
				return nil, err
			}
			tok.SetAuthHeader(req)
		}
		return t.base.RoundTrip(req)
	}
	resp, err := send()
	if err != nil || resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp, err
	}
	if err := t.handler.Authorize(r.Context(), r, resp); err != nil { // closes resp.Body
		return nil, err
	}
	return send()
}

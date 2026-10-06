package sandbox

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for larik inside a Linux sandbox,
// where bubblewrap runs the binary's bridge command before the script.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == BridgeCommand {
		os.Exit(BridgeMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestProxyAllows(t *testing.T) {
	p := &proxy{}
	for _, d := range []string{"GO.dev", "*.npmjs.org", ".crates.io", "127.0.0.1"} {
		d = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(d), "*."), ".")
		p.allowed = append(p.allowed, d)
	}
	for host, want := range map[string]bool{
		"go.dev": true, "proxy.go.dev": true, "Go.Dev.": true,
		"notgo.dev": false, "go.dev.evil.com": false,
		"registry.npmjs.org": true, "npmjs.org": true,
		"index.crates.io": true,
		"127.0.0.1":       true, "127.0.0.2": false,
	} {
		if got := p.allows(host); got != want {
			t.Errorf("allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func newProxy(t *testing.T, unixPath string, domains ...string) *proxy {
	t.Helper()
	p, err := startProxy(domains, unixPath)
	if unixPath != "" && errors.Is(err, syscall.EPERM) {
		// An enclosing sandbox (say, an agent running these tests) may
		// forbid binding Unix sockets; that says nothing about the proxy.
		t.Skipf("can't bind a Unix socket here, probably inside another sandbox: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func client(p *proxy) *http.Client {
	u, _ := url.Parse(p.URL())
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(u),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
}

func TestProxyForwardsAllowedAndRefusesTheRest(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "plain ok") }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tls ok") }))
	defer secure.Close()

	p := newProxy(t, "", "127.0.0.1")
	c := client(p)
	for _, target := range []struct{ url, want string }{{plain.URL, "plain ok"}, {secure.URL, "tls ok"}} {
		resp, err := c.Get(target.url)
		if err != nil {
			t.Fatalf("%s: %v", target.url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != target.want {
			t.Errorf("%s: got %q", target.url, body)
		}
	}

	start := time.Now()
	// Refused before any connection is made, so no network is needed.
	resp, err := c.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "example.com is not in sandbox.allowed_domains") {
		t.Errorf("plain http to a refused host: %d %s", resp.StatusCode, body)
	}
	if _, err := c.Get("https://evil.example.org/"); err == nil {
		t.Error("CONNECT to a refused host should fail")
	}
	if got := p.blockedSince(start); !slices.Equal(got, []string{"example.com", "evil.example.org"}) {
		t.Errorf("blocked hosts: %v", got)
	}
	if got := p.blockedSince(time.Now()); len(got) != 0 {
		t.Errorf("nothing was refused after now: %v", got)
	}
}

func TestProxyRefusesMetadataAddress(t *testing.T) {
	p := newProxy(t, "", "169.254.169.254")
	resp, err := client(p).Get("http://169.254.169.254/latest/meta-data")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("even an allowed metadata address must not be reached")
	}
}

// TestBridge covers the Linux path on any OS: a TCP listener forwarding to
// the proxy's Unix socket, as the bridge does inside the namespace.
func TestBridge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "through the bridge") }))
	defer upstream.Close()
	dir, err := os.MkdirTemp("", "lb") // short: Unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "p.sock")
	p := newProxy(t, sock, "127.0.0.1")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go bridge(ln, sock)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstream.URL, strings.TrimPrefix(upstream.URL, "http://"))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "through the bridge" {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
	_ = p
}

func TestSandboxAllowlist(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl needed")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "allowed ok") }))
	defer upstream.Close()
	sb, root, _ := newTest(t, Config{AllowedDomains: []string{"127.0.0.1"}})
	if sb.proxy == nil {
		t.Fatal("no proxy")
	}
	if !strings.Contains(sb.Summary(), "only to 127.0.0.1") {
		t.Errorf("summary: %s", sb.Summary())
	}
	// --noproxy '' sends even a loopback URL through the proxy, as a real
	// domain would go.
	if out, err := run(t, sb, root, "curl -sS --noproxy '' "+upstream.URL); err != nil || out != "allowed ok" {
		t.Errorf("allowed host through the proxy: %v %q", err, out)
	}
	start := time.Now()
	out, err := run(t, sb, root, "curl -sS https://example.com/")
	if err == nil || !strings.Contains(out, "403") {
		t.Errorf("a refused host should fail with the proxy's 403: %v %q", err, out)
	}
	if got := sb.NetworkBlocked(start); !slices.Equal(got, []string{"example.com"}) {
		t.Errorf("blocked: %v", got)
	}
	// Direct connections still fail: the proxy is the only way out.
	if out, _ := run(t, sb, root, "curl -sS --noproxy '*' --max-time 3 http://1.1.1.1/ && echo CONN''ECTED"); strings.Contains(out, "CONNECTED") {
		t.Error("a direct connection got out of the sandbox")
	}
}

func TestBwrapArgsWithBridge(t *testing.T) {
	dir := t.TempDir()
	b := &Sandbox{kind: "bubblewrap", root: dir, tmpDir: dir, exe: "/usr/bin/larik", proxy: &proxy{addr: "127.0.0.1:4242"}}
	args := strings.Join(b.bwrapArgs("make", dir), " ")
	want := "--unshare-net --chdir " + dir + " -- /usr/bin/larik " + BridgeCommand + " " + filepath.Join(dir, "proxy.sock") + " 4242 -- bash -c make"
	if !strings.Contains(args, want) {
		t.Errorf("bwrap args:\n%s\nwant %s", args, want)
	}
}

func TestProxyEnv(t *testing.T) {
	env := proxyEnv([]string{"PATH=/bin", "https_proxy=http://corp:3128", "NO_PROXY=corp"}, "http://127.0.0.1:9")
	got := strings.Join(env, " ")
	for _, want := range []string{"PATH=/bin", "HTTPS_PROXY=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9", "NO_PROXY=localhost,127.0.0.1,::1", "NODE_USE_ENV_PROXY=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "corp") {
		t.Errorf("the user's own proxy settings must be replaced: %s", got)
	}
}

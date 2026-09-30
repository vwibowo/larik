package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"larik/internal/web"
)

// proxy is the HTTP proxy sandboxed commands reach the network through
// when only some domains are allowed (Config.AllowedDomains). It tunnels
// CONNECT (https, and anything else over TLS) and forwards plain http
// requests, to allowed hosts only, and records the hosts it refuses so the
// bash tool can say what was blocked.
type proxy struct {
	allowed []string // normalized: lowercase, no "*." or leading dot
	addr    string   // 127.0.0.1:port
	servers []*http.Server
	fwd     *http.Transport

	mu      sync.Mutex
	blocked []blockedHost
}

type blockedHost struct {
	at   time.Time
	host string
}

const maxBlocked = 100

// startProxy listens on a loopback port and, when unixPath is set, also on
// that Unix socket (the way in from a Linux network namespace).
func startProxy(domains []string, unixPath string) (*proxy, error) {
	p := &proxy{}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(strings.TrimPrefix(d, "*."), ".")
		if d != "" && !slices.Contains(p.allowed, d) {
			p.allowed = append(p.allowed, d)
		}
	}
	p.fwd = &http.Transport{Proxy: nil, DialContext: p.dial, ForceAttemptHTTP2: false, ResponseHeaderTimeout: 60 * time.Second}

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.addr = tcp.Addr().String()
	listeners := []net.Listener{tcp}
	if unixPath != "" {
		ul, err := net.Listen("unix", unixPath)
		if err != nil {
			tcp.Close()
			return nil, err
		}
		listeners = append(listeners, ul)
	}
	for _, ln := range listeners {
		srv := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
		p.servers = append(p.servers, srv)
		go srv.Serve(ln) //nolint:errcheck // ends with Close
	}
	return p, nil
}

func (p *proxy) Close() {
	for _, srv := range p.servers {
		srv.Close()
	}
	p.fwd.CloseIdleConnections()
}

// URL is the proxy's address for HTTP_PROXY and friends.
func (p *proxy) URL() string { return "http://" + p.addr }

// Port is the proxy's loopback port.
func (p *proxy) Port() string {
	_, port, _ := net.SplitHostPort(p.addr)
	return port
}

// allows reports whether host is an allowed domain or a subdomain of one.
// An IP address must be listed itself.
func (p *proxy) allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range p.allowed {
		if host == d || (net.ParseIP(host) == nil && strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func (p *proxy) block(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = append(p.blocked, blockedHost{time.Now(), host})
	if len(p.blocked) > maxBlocked {
		p.blocked = p.blocked[len(p.blocked)-maxBlocked:]
	}
}

// blockedSince lists the distinct hosts refused since t.
func (p *proxy) blockedSince(t time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, b := range p.blocked {
		if !b.at.Before(t) && !slices.Contains(out, b.host) {
			out = append(out, b.host)
		}
	}
	return out
}

// dial connects to an allowed host, refusing link-local and cloud metadata
// addresses however the name resolves.
func (p *proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := web.CheckHost(ctx, host); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, network, addr)
}

func (p *proxy) refuse(w http.ResponseWriter, host string) {
	p.block(host)
	http.Error(w, fmt.Sprintf("larik sandbox: %s is not in sandbox.allowed_domains", host), http.StatusForbidden)
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "larik sandbox proxy: expected an absolute URL", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	if !p.allows(host) {
		p.refuse(w, host)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := p.fwd.RoundTrip(out)
	if err != nil {
		http.Error(w, "larik sandbox proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// tunnel handles CONNECT: after the check it's a byte pipe, so TLS goes end
// to end and the proxy never sees plaintext.
func (p *proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "larik sandbox proxy: bad CONNECT target", http.StatusBadRequest)
		return
	}
	if !p.allows(host) {
		p.refuse(w, host)
		return
	}
	upstream, err := p.dial(r.Context(), "tcp", r.Host)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, web.ErrBlockedAddress) {
			status = http.StatusForbidden
		}
		http.Error(w, "larik sandbox proxy: "+err.Error(), status)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "larik sandbox proxy: can't tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	// Bytes the client sent after the CONNECT headers are already buffered.
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		_, _ = upstream.Write(pending)
	}
	pipe(client, upstream)
}

// pipe copies both ways until either side closes, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

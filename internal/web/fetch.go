// Package web implements the web_fetch and web_search tools.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxBodyBytes = 5 << 20
	fetchTimeout = 30 * time.Second
	cacheTTL     = 15 * time.Minute
	userAgent    = "Larik/0.1 (terminal coding agent; +https://github.com/)"
)

// Page is a fetched document converted to text.
type Page struct {
	URL         string
	ContentType string
	Title       string
	Text        string // Markdown for HTML, raw text otherwise
	// RedirectTo is set when the server redirected to another host; the
	// fetcher stops there so per-domain permissions can't be sidestepped.
	RedirectTo string
	Status     int
}

// Fetcher downloads pages with SSRF protections and a short-lived cache.
type Fetcher struct {
	client *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	page Page
	at   time.Time
}

// ErrBlockedAddress is returned for link-local and similar addresses
// (e.g. cloud metadata endpoints) that must never be fetched.
var ErrBlockedAddress = errors.New("address not allowed")

func NewFetcher() *Fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: checkedProxy(http.ProxyFromEnvironment),
		// Check the resolved address, so DNS tricks can't reach blocked IPs.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if blockedIP(ip.IP) {
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip.IP)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	f := &Fetcher{cache: map[string]cached{}}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
				return http.ErrUseLastResponse // report it instead of following
			}
			return nil
		},
	}
	return f
}

// blockedIP rejects link-local (incl. 169.254.169.254 metadata), multicast
// and unspecified addresses. Loopback and private ranges stay reachable
// because local dev servers are a legitimate target; fetches ask first.
// checkedProxy wraps a proxy function with the address check that
// DialContext can't make when a proxy is used: it then dials the proxy,
// not the target. The proxy resolves the name itself, so this is best
// effort: a name that doesn't resolve here is left to the proxy.
func checkedProxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxy, err := next(req)
		if proxy == nil || err != nil {
			return proxy, err
		}
		return proxy, checkTarget(req)
	}
}

func checkTarget(req *http.Request) error {
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
		}
		return nil
	}
	if ips, err := net.DefaultResolver.LookupIPAddr(req.Context(), host); err == nil {
		for _, ip := range ips {
			if blockedIP(ip.IP) {
				return fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip.IP)
			}
		}
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.Equal(net.ParseIP("100.100.100.200")) || // Alibaba Cloud metadata
		ip.Equal(net.ParseIP("fd00:ec2::254")) // AWS metadata over IPv6
}

// Fetch downloads rawURL and converts it to text.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Page{}, fmt.Errorf("invalid URL %q: only http and https URLs are supported", rawURL)
	}
	key := u.String()
	f.mu.Lock()
	if c, ok := f.cache[key]; ok && time.Since(c.at) < cacheTTL {
		f.mu.Unlock()
		return c.page, nil
	}
	f.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/markdown,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := f.client.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()

	page := Page{URL: resp.Request.URL.String(), Status: resp.StatusCode}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, err := resp.Location()
		if err == nil {
			page.RedirectTo = loc.String()
			return page, nil
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Page{}, err
	}
	truncated := len(body) > maxBodyBytes
	if truncated {
		body = body[:maxBodyBytes]
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = http.DetectContentType(body)
		mediaType, _, _ = mime.ParseMediaType(mediaType)
	}
	page.ContentType = mediaType

	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		page.Title, page.Text, err = htmlToMarkdown(body, resp.Request.URL)
		if err != nil {
			return Page{}, fmt.Errorf("converting HTML: %w", err)
		}
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") || mediaType == "application/javascript":
		page.Text = string(body)
	default:
		return Page{}, fmt.Errorf("unsupported content type %q (only HTML and text are supported)", mediaType)
	}
	if truncated {
		page.Text += "\n\n[download stopped at 5 MB]"
	}
	if resp.StatusCode >= 400 {
		page.Text = fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, http.StatusText(resp.StatusCode), page.Text)
	}

	f.mu.Lock()
	f.cache[key] = cached{page, time.Now()}
	f.mu.Unlock()
	return page, nil
}

// noise elements are removed before conversion.
var noise = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true, atom.Iframe: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true, atom.Form: true,
	atom.Button: true, atom.Template: true, atom.Object: true, atom.Canvas: true,
}

var noiseRoles = map[string]bool{"navigation": true, "banner": true, "contentinfo": true, "complementary": true, "search": true}

func htmlToMarkdown(body []byte, base *url.URL) (title, md string, err error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	title = strings.Join(strings.Fields(textOf(find(doc, atom.Title))), " ")

	// Prefer the main content region when the page marks one.
	root := find(doc, atom.Main)
	if root == nil {
		root = find(doc, atom.Article)
	}
	if root == nil {
		root = find(doc, atom.Body)
	}
	if root == nil {
		root = doc
	}
	// Resolve links against the page URL (or its <base href>) so relative
	// and #fragment links point where the browser would take them.
	if b := find(doc, atom.Base); b != nil {
		if href := attr(b, "href"); href != "" {
			if u, err := base.Parse(href); err == nil {
				base = u
			}
		}
	}
	prune(root)
	resolveLinks(root, base)

	out, err := htmltomarkdown.ConvertNode(root, converter.WithDomain(base.Scheme+"://"+base.Host))
	if err != nil {
		return title, "", err
	}
	return title, strings.TrimSpace(collapseBlank(string(out))), nil
}

func prune(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		decorative := c.Type == html.ElementNode && c.DataAtom == atom.Img && strings.TrimSpace(attr(c, "alt")) == ""
		if c.Type == html.ElementNode && (decorative || noise[c.DataAtom] || noiseRoles[attr(c, "role")] || attr(c, "aria-hidden") == "true" || hasAttr(c, "hidden")) {
			n.RemoveChild(c)
		} else if c.Type == html.CommentNode {
			n.RemoveChild(c)
		} else {
			prune(c)
		}
		c = next
	}
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return strings.ToLower(a.Val)
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func collapseBlank(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return strings.Join(out, "\n")
}

func resolveLinks(n *html.Node, base *url.URL) {
	if n.Type == html.ElementNode {
		for i, a := range n.Attr {
			if a.Key != "href" && a.Key != "src" {
				continue
			}
			v := strings.TrimSpace(a.Val)
			if v == "" || strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "data:") {
				continue
			}
			if u, err := base.Parse(v); err == nil {
				n.Attr[i].Val = u.String()
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		resolveLinks(c, base)
	}
}

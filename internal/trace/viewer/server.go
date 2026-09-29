// Package viewer serves a trace to the browser: one embedded page with a
// timeline, a ledger of records and an inspector, reading the trace
// directory as it grows.
package viewer

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"larik/internal/trace"
)

//go:embed viewer.html
var page string

// Options describe the trace being viewed.
type Options struct {
	Dir   string // the trace directory
	Title string // the session, for the page header
}

// liveWindow is how recently the trace must have changed to count as live.
const liveWindow = 30 * time.Second

// maxExportBody caps each HTTP body inlined into an exported page.
const maxExportBody = 1 << 20

var bodyName = regexp.MustCompile(`^[0-9]{4}-(request\.json|response\.txt)$`)

// Server serves one trace on a loopback address, to a URL carrying a
// random token, since traces hold prompts and file contents.
type Server struct {
	opts  Options
	token string
	ln    net.Listener
	srv   *http.Server
}

// Start listens on 127.0.0.1:port (0 picks a free port) and serves.
func Start(o Options, port int) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{opts: o, token: hex.EncodeToString(b), ln: ln}
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return s, nil
}

// URL is the page's address, token included.
func (s *Server) URL() string { return "http://" + s.ln.Addr().String() + "/?t=" + s.token }

// Close stops serving.
func (s *Server) Close() error { return s.srv.Close() }

// Handler is the page and its API. Every request must carry the token,
// and the Host must be the loopback address, which stops other sites
// reaching it through DNS rebinding.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, render(map[string]any{"mode": "live", "title": s.opts.Title}))
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		recs, err := ReadRecords(s.opts.Dir, after)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"records": recs, "live": live(s.opts.Dir)})
	})
	mux.HandleFunc("/api/body/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/body/")
		if !bodyName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeFile(w, r, filepath.Join(s.opts.Dir, "http", name))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		t := r.URL.Query().Get("t")
		if t == "" {
			t = r.Header.Get("X-Trace-Token")
		}
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
			http.Error(w, "missing or wrong token; open the URL larik printed", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

// ReadRecords returns the trace's records after seq, as raw JSON.
func ReadRecords(dir string, after int64) ([]json.RawMessage, error) {
	f, err := os.Open(filepath.Join(dir, trace.EventsFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []json.RawMessage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(line, &head) != nil || head.Seq <= after {
			continue // a line still being written is picked up next time
		}
		out = append(out, append(json.RawMessage(nil), line...))
	}
	return out, sc.Err()
}

func live(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, trace.EventsFile))
	return err == nil && time.Since(fi.ModTime()) < liveWindow
}

// Export writes the trace as one self-contained page: records and HTTP
// bodies inline, each body capped at 1 MB.
func Export(o Options, w io.Writer) error {
	recs, err := ReadRecords(o.Dir, 0)
	if err != nil {
		return err
	}
	bodies := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(o.Dir, "http"))
	for _, e := range entries {
		if !bodyName.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(o.Dir, "http", e.Name()))
		if err != nil {
			continue
		}
		if len(data) > maxExportBody {
			data = append(data[:maxExportBody:maxExportBody], fmt.Sprintf("\n… (cut at 1 MB of %d bytes in the export)", len(data))...)
		}
		bodies[e.Name()] = string(data)
	}
	_, err = io.WriteString(w, render(map[string]any{"mode": "static", "title": o.Title, "records": recs, "bodies": bodies}))
	return err
}

// render puts the page's boot data in place. The JSON is escaped so no
// "</script>" in a trace can end the script early.
func render(boot map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	enc.Encode(boot)
	return strings.Replace(page, "/*BOOT*/null", strings.TrimSpace(buf.String()), 1)
}

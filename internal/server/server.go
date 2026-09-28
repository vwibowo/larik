// Package server exposes agents over a local HTTP API with a Server-Sent
// Events stream per session, so editors, web UIs and scripts can drive
// Larik. See README for the endpoint reference.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/session"
)

// Options configure a Server.
type Options struct {
	Token string // required bearer token
	// Defaults apply to sessions created without explicit settings.
	Defaults app.Options
	// AllowAnyHost disables the Host header check that guards a
	// loopback-only server against DNS rebinding.
	AllowAnyHost bool
	// Keepalive is the SSE comment interval (default 15s).
	Keepalive time.Duration
}

type Server struct {
	app  *app.App
	opts Options

	mu       sync.Mutex
	sessions map[string]*live
	opening  map[string]bool // session files being opened
	shutdown bool
}

func New(a *app.App, opts Options) *Server {
	if opts.Keepalive == 0 {
		opts.Keepalive = 15 * time.Second
	}
	return &Server{app: a, opts: opts, sessions: map[string]*live{}, opening: map[string]bool{}}
}

// Handler returns the HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/info", s.info)
	api.HandleFunc("GET /v1/sessions", s.listSessions)
	api.HandleFunc("POST /v1/sessions", s.createSession)
	api.HandleFunc("GET /v1/sessions/{id}", s.withLive(s.getSession))
	api.HandleFunc("PATCH /v1/sessions/{id}", s.withLive(s.patchSession))
	api.HandleFunc("DELETE /v1/sessions/{id}", s.closeSession)
	api.HandleFunc("GET /v1/sessions/{id}/messages", s.messages)
	api.HandleFunc("POST /v1/sessions/{id}/fork", s.fork)
	api.HandleFunc("GET /v1/sessions/{id}/events", s.withLive(s.events))
	api.HandleFunc("POST /v1/sessions/{id}/prompt", s.withLive(s.prompt))
	api.HandleFunc("POST /v1/sessions/{id}/cancel", s.withLive(s.cancel))
	api.HandleFunc("GET /v1/sessions/{id}/permissions", s.withLive(s.listPerms))
	api.HandleFunc("POST /v1/sessions/{id}/permissions/{rid}", s.withLive(s.answerPerm))
	api.HandleFunc("POST /v1/sessions/{id}/compact", s.withLive(s.compact))
	api.HandleFunc("POST /v1/sessions/{id}/undo", s.withLive(s.undo))
	api.HandleFunc("POST /v1/sessions/{id}/clear", s.withLive(s.clear))
	api.HandleFunc("GET /v1/sessions/{id}/tasks", s.withLive(s.tasks))
	api.HandleFunc("DELETE /v1/sessions/{id}/tasks/{tid}", s.withLive(s.stopTask))
	mux.Handle("/", s.auth(api))
	return s.checkHost(mux)
}

// Close ends every loaded session. The Server rejects new sessions after.
func (s *Server) Close() {
	s.mu.Lock()
	s.shutdown = true
	all := make([]*live, 0, len(s.sessions))
	for _, l := range s.sessions {
		all = append(all, l)
	}
	s.sessions = map[string]*live{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, l := range all {
		wg.Add(1)
		go func() { defer wg.Done(); l.close() }()
	}
	wg.Wait()
}

// ---- middleware ----

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") {
			got = r.URL.Query().Get("token") // EventSource can't set headers
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost rejects requests whose Host isn't a loopback name, so a web
// page can't reach the server through DNS rebinding.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.opts.AllowAnyHost {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			host = strings.Trim(host, "[]")
			if ip := net.ParseIP(host); !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
				writeErr(w, http.StatusForbidden, "host not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withLive(h func(http.ResponseWriter, *http.Request, *live)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := s.lookup(r.PathValue("id"))
		if l == nil {
			writeErr(w, http.StatusNotFound, "session not loaded; open it with POST /v1/sessions {\"resume\": id}")
			return
		}
		h(w, r, l)
	}
}

func (s *Server) lookup(id string) *live {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// ---- sessions ----

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   s.app.Version,
		"cwd":       s.app.Cwd,
		"providers": providers.Names(),
		"sandbox":   s.app.Sandbox != nil,
	})
}

type sessionInfo struct {
	ID       string    `json:"id"`
	Title    string    `json:"title,omitempty"`
	Modified time.Time `json:"modified"`
	ForkOf   string    `json:"fork_of,omitempty"`
	Loaded   bool      `json:"loaded"`
	Busy     bool      `json:"busy"`
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	infos, err := session.List(s.app.SessionDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]sessionInfo, 0, len(infos))
	for _, in := range infos {
		si := sessionInfo{ID: in.ID, Title: in.Title, Modified: in.Modified, ForkOf: in.ForkOf}
		if l := s.lookup(in.ID); l != nil {
			si.Loaded, si.Busy = true, l.isBusy()
		}
		out = append(out, si)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

type createReq struct {
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Mode     string `json:"mode"`
	Resume   string `json:"resume"`
	Continue bool   `json:"continue"`
}

// createSession starts a new session, or loads (resume/continue) an
// existing one. Loading an already-loaded session returns it as is.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !readJSON(w, r, &req) {
		return
	}
	o := app.Options{Model: req.Model, Effort: req.Effort, Mode: req.Mode}
	var id string
	if req.Resume != "" || req.Continue {
		path, err := s.resolve(req.Resume, req.Continue)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		o.ResumeID = id
	} else {
		d := s.opts.Defaults
		o.Model, o.Effort, o.Mode = or(o.Model, d.Model), or(o.Effort, d.Effort), or(o.Mode, d.Mode)
	}

	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, "server is shutting down")
		return
	}
	if l, ok := s.sessions[id]; ok && id != "" {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, s.describe(l))
		return
	}
	if s.opening[id] && id != "" {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "session is being opened by another request")
		return
	}
	s.opening[id] = true
	s.mu.Unlock()

	sess, err := s.app.Open(o)

	s.mu.Lock()
	delete(s.opening, id)
	s.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if l, ok := s.register(w, sess); ok {
		writeJSON(w, http.StatusCreated, s.describe(l))
	}
}

// register makes an opened session live, unless the server is shutting down.
func (s *Server) register(w http.ResponseWriter, sess *app.Session) (*live, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdown {
		sess.Close("other")
		writeErr(w, http.StatusServiceUnavailable, "server is shutting down")
		return nil, false
	}
	l := newLive(sess)
	s.sessions[l.id] = l
	return l, true
}

// fork branches a session (loaded or not) into a new, loaded one. With
// "at" it keeps the messages before that index, which must be a prompt
// (see GET …/messages); the response then carries that prompt's text so
// a client can offer it for editing. Settings default to the source's
// current ones when it is loaded.
func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		At     *int   `json:"at"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
		Mode   string `json:"mode"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	path, err := session.Find(s.app.SessionDir, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	o := app.Options{ResumeID: id, Fork: true, ForkAt: req.At, Model: req.Model, Effort: req.Effort, Mode: req.Mode}
	src := s.lookup(id)
	if src != nil {
		eff := string(src.a.Effort())
		if eff == "" {
			eff = "default"
		}
		o.Model = or(o.Model, src.a.ProviderName()+"/"+src.a.Model())
		o.Effort = or(o.Effort, eff)
		o.Mode = or(o.Mode, string(src.a.Perms().Mode()))
	}
	var sess *app.Session
	open := func(context.Context) error {
		var err error
		sess, err = s.app.Open(o)
		return err
	}
	if src != nil {
		// A running turn may be mid tool call: branch only from a settled
		// state, and keep the source idle while it is copied.
		err = src.idleDo(r.Context(), open)
	} else {
		err = open(r.Context())
	}
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	var prompt string
	if req.At != nil {
		if st, err := session.Load(path); err == nil && *req.At < len(st.All) {
			prompt = st.All[*req.At].Text()
		}
	}
	l, ok := s.register(w, sess)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		sessionState
		Prompt string `json:"prompt,omitempty"`
	}{s.describe(l), prompt})
}

func (s *Server) resolve(id string, latest bool) (string, error) {
	if latest {
		infos, err := session.List(s.app.SessionDir)
		if err != nil {
			return "", err
		}
		if len(infos) == 0 {
			return "", errors.New("no previous session in this directory")
		}
		return infos[0].Path, nil
	}
	return session.Find(s.app.SessionDir, id)
}

type sessionState struct {
	ID          string          `json:"id"`
	ForkOf      string          `json:"fork_of,omitempty"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Effort      string          `json:"effort,omitempty"`
	Mode        string          `json:"mode"`
	Busy        bool            `json:"busy"`
	LastSeq     int64           `json:"last_seq"`
	Usage       agent.UsageInfo `json:"usage"`
	Permissions int             `json:"pending_permissions"`
	Background  int             `json:"running_tasks"`
}

func (s *Server) describe(l *live) sessionState {
	return sessionState{
		ID:          l.id,
		ForkOf:      l.s.ForkOf,
		Provider:    l.a.ProviderName(),
		Model:       l.a.Model(),
		Effort:      string(l.a.Effort()),
		Mode:        string(l.a.Perms().Mode()),
		Busy:        l.isBusy(),
		LastSeq:     l.bus.last(),
		Usage:       l.a.Stats(),
		Permissions: len(l.pendingPerms()),
		Background:  l.a.RunningBackground(),
	}
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, l *live) {
	writeJSON(w, http.StatusOK, s.describe(l))
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request, l *live) {
	var req struct {
		Model  *string `json:"model"`
		Effort *string `json:"effort"`
		Mode   *string `json:"mode"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Validate everything before changing anything.
	var (
		res  providers.Resolved
		eff  llm.Effort
		mode permission.Mode
		err  error
	)
	if req.Model != nil {
		if res, err = s.app.Resolve(s.app.Cfg, *req.Model); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Effort != nil {
		if eff, err = app.ParseEffort(*req.Effort); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Mode != nil {
		if mode, err = permission.ParseMode(*req.Mode); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Model != nil {
		l.a.SetModel(res.Provider, res.Model)
	}
	if req.Effort != nil {
		l.a.SetEffort(eff)
	}
	if req.Mode != nil {
		l.a.Perms().SetMode(mode)
	}
	writeJSON(w, http.StatusOK, s.describe(l))
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	l := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if l == nil {
		writeErr(w, http.StatusNotFound, "session not loaded")
		return
	}
	l.close()
	w.WriteHeader(http.StatusNoContent)
}

// messages returns the session's full transcript from its file, so it
// works for sessions that aren't loaded too.
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	path, err := session.Find(s.app.SessionDir, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	st, err := session.Load(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	msgs := st.All
	if msgs == nil {
		msgs = []llm.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// ---- runs ----

type promptReq struct {
	Text string `json:"text"`
	// Wait blocks until the session is idle again and returns the final
	// answer, for simple scripted use. Permission prompts still need an
	// answer from some client (or a permissive mode).
	Wait bool `json:"wait"`
}

func (s *Server) prompt(w http.ResponseWriter, r *http.Request, l *live) {
	var req promptReq
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text is required")
		return
	}
	var ch chan Event
	if req.Wait {
		// Subscribe first so no event of this run is missed.
		_, ch, _ = l.bus.subscribe(l.bus.last())
		defer l.bus.unsubscribe(ch)
	}
	start := l.bus.last()
	if err := l.prompt(req.Text); err != nil {
		code := http.StatusConflict
		if !errors.Is(err, errBusy) {
			code = http.StatusGone
		}
		writeErr(w, code, err.Error())
		return
	}
	if !req.Wait {
		writeJSON(w, http.StatusAccepted, map[string]any{"session": l.id, "after_seq": start})
		return
	}

	var res struct {
		Session    string           `json:"session"`
		Text       string           `json:"text"`
		StopReason string           `json:"stop_reason"`
		Error      string           `json:"error,omitempty"`
		Usage      *agent.UsageInfo `json:"usage,omitempty"`
	}
	res.Session = l.id
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				res.Error = "event stream closed"
				writeJSON(w, http.StatusOK, res)
				return
			}
			if e.Agent != "" {
				continue
			}
			switch e.Kind {
			case agent.EvAssistant:
				if t := e.Message.Text(); t != "" {
					res.Text = t
				}
			case agent.EvError:
				res.Error = e.Text
			case agent.EvDone:
				res.StopReason = e.StopReason
			case agent.EvUsage:
				res.Usage = e.Usage
			case EvStatus:
				if !*e.Busy {
					writeJSON(w, http.StatusOK, res)
					return
				}
			}
		case <-r.Context().Done():
			return // the run continues; cancel it explicitly if wanted
		}
	}
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, l *live) {
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": l.interrupt()})
}

func (s *Server) listPerms(w http.ResponseWriter, r *http.Request, l *live) {
	writeJSON(w, http.StatusOK, map[string]any{"permissions": l.pendingPerms()})
}

func (s *Server) answerPerm(w http.ResponseWriter, r *http.Request, l *live) {
	var req struct {
		Allow  bool   `json:"allow"`
		Always bool   `json:"always"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	always := req.Always && req.Allow
	reply := agent.PermissionReply{Allow: req.Allow, Always: always, Reason: req.Reason}
	var persisted chan error
	if always {
		persisted = make(chan error, 1)
		reply.Persisted = persisted
	}
	if !l.answer(r.PathValue("rid"), reply) {
		writeErr(w, http.StatusNotFound, "no pending permission request "+r.PathValue("rid"))
		return
	}
	if always {
		select {
		case err := <-persisted:
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"allowed": true, "persisted": false, "error": err.Error()})
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"allowed": true, "persisted": true})
			}
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			writeJSON(w, http.StatusGatewayTimeout, map[string]any{"allowed": true, "persisted": false, "error": "approval result was not received"})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) compact(w http.ResponseWriter, r *http.Request, l *live) {
	var summary string
	err := l.idleDo(r.Context(), func(ctx context.Context) error {
		var err error
		summary, err = l.a.Compact(ctx)
		return err
	})
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	l.publish(agent.Event{Kind: agent.EvCompacted, Summary: summary}, false)
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary})
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request, l *live) {
	var paths []string
	err := l.idleDo(r.Context(), func(context.Context) error {
		var err error
		paths, err = l.a.Undo()
		return err
	})
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": paths})
}

func (s *Server) clear(w http.ResponseWriter, r *http.Request, l *live) {
	if err := l.idleDo(r.Context(), func(context.Context) error { l.a.Clear(); return nil }); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type taskInfo struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Status   string     `json:"status"`
	Result   string     `json:"result,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request, l *live) {
	out := []taskInfo{}
	for _, t := range l.a.BackgroundTasks() {
		ti := taskInfo{ID: t.ID, Label: t.Label, Status: string(t.Status), Result: t.Result, Started: t.Started}
		if !t.Finished.IsZero() {
			ti.Finished = &t.Finished
		}
		out = append(out, ti)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request, l *live) {
	if err := l.a.StopBackground(r.PathValue("tid")); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- events ----

// events streams a session's events as SSE. Clients resume after a
// disconnect with the Last-Event-ID header (or ?after=seq); events older
// than the replay buffer are reported with a "gap" comment.
func (s *Server) events(w http.ResponseWriter, r *http.Request, l *live) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	after := l.bus.last() // by default, only new events
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	replay, ch, gap := l.bus.subscribe(after)
	defer l.bus.unsubscribe(ch)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	if gap {
		fmt.Fprint(w, ": gap: some events were dropped from the replay buffer; refetch messages\n\n")
	}
	for _, e := range replay {
		writeEvent(w, e)
	}
	fl.Flush()

	tick := time.NewTicker(s.opts.Keepalive)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return // session closed or client too slow; client reconnects
			}
			writeEvent(w, e)
			// Coalesce whatever else is queued into one flush.
			for n := len(ch); n > 0; n-- {
				if e, ok = <-ch; !ok {
					fl.Flush()
					return
				}
				writeEvent(w, e)
			}
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e Event) {
	data, _ := json.Marshal(e)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, data)
}

// ---- helpers ----

func statusFor(err error) int {
	if errors.Is(err, errBusy) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

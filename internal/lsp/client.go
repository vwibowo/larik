package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// client is one running language server for one project root.
type client struct {
	name string
	root string
	cfg  ServerConfig
	cmd  *exec.Cmd
	conn *conn
	log  *os.File

	mu      sync.Mutex
	docs    map[string]int // uri -> version
	diags   map[string][]Diagnostic
	seq     map[string]int64 // uri -> publish sequence number
	counter int64
	changed chan struct{} // closed and replaced on every publish
	started time.Time
	misses  int // consecutive edits that got no publish before the deadline
}

func startClient(ctx context.Context, name, root string, cfg ServerConfig, logPath string) (*client, error) {
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &client{name: name, root: root, cfg: cfg, cmd: cmd,
		docs: map[string]int{}, diags: map[string][]Diagnostic{}, seq: map[string]int64{}, changed: make(chan struct{}), started: time.Now()}
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			c.log, cmd.Stderr = f, f
		}
	}
	if err := cmd.Start(); err != nil {
		c.closeLog()
		return nil, err
	}
	c.conn = newConn(stdout, stdin)
	c.conn.onNotify = c.handleNotify
	c.conn.onRequest = c.handleRequest
	c.conn.start()
	go func() { _ = cmd.Wait(); c.conn.shutdown(errClosed) }()

	if err := c.initialize(ctx); err != nil {
		c.kill()
		return nil, fmt.Errorf("%s: initialize: %w", name, err)
	}
	return c, nil
}

func (c *client) initialize(ctx context.Context) error {
	rootURI := pathToURI(c.root)
	params := map[string]any{
		"processId":        os.Getpid(),
		"clientInfo":       map[string]any{"name": "larik"},
		"rootUri":          rootURI,
		"rootPath":         c.root,
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": c.root}},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true, "dynamicRegistration": false},
				"publishDiagnostics": map[string]any{"relatedInformation": false, "versionSupport": true},
				"definition":         map[string]any{"linkSupport": true},
				"references":         map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{
				"workspaceFolders": true,
				"configuration":    true,
				"symbol":           map[string]any{},
			},
			"window": map[string]any{"workDoneProgress": false},
		},
	}
	if c.cfg.InitializationOptions != nil {
		params["initializationOptions"] = c.cfg.InitializationOptions
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.conn.call(ctx, "initialize", params, nil); err != nil {
		return err
	}
	return c.conn.notify("initialized", map[string]any{})
}

func (c *client) handleNotify(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p publishDiagnostics
	if json.Unmarshal(params, &p) != nil {
		return
	}
	c.mu.Lock()
	c.diags[p.URI] = p.Diagnostics
	c.counter++
	c.seq[p.URI] = c.counter
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

// handleRequest answers the server->client requests servers commonly send.
func (c *client) handleRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		return make([]any, len(p.Items)), nil // null for every item: server defaults
	case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability":
		return nil, nil
	case "workspace/workspaceFolders":
		return []map[string]any{{"uri": pathToURI(c.root), "name": c.root}}, nil
	case "window/showMessageRequest":
		return nil, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not supported: " + method}
}

// sync opens or updates a document with its current contents from disk.
// It returns the publish counter observed before the change, for waitFor.
func (c *client) sync(path, languageID string) (int64, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	uri := pathToURI(path)
	c.mu.Lock()
	before := c.counter
	version, open := c.docs[uri]
	version++
	c.docs[uri] = version
	c.mu.Unlock()

	if !open {
		err = c.conn.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": languageID, "version": version, "text": string(data)},
		})
	} else {
		err = c.conn.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": string(data)}},
		})
		if err == nil { // some servers only re-check on save
			err = c.conn.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": string(data)})
		}
	}
	return before, !open, err
}

// waitFor waits until diagnostics for uri are published after `after`,
// then lingers briefly so a follow-up publish (e.g. semantic after
// syntax errors) is included. It reports whether anything was published.
func (c *client) waitFor(ctx context.Context, uri string, after int64, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		got := c.seq[uri] > after
		ch := c.changed
		c.mu.Unlock()
		if got {
			break
		}
		select {
		case <-ch:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		case <-c.conn.done:
			return false
		}
	}
	settle := time.NewTimer(300 * time.Millisecond)
	defer settle.Stop()
	for {
		c.mu.Lock()
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
			settle.Reset(300 * time.Millisecond)
		case <-settle.C:
			return true
		case <-ctx.Done():
			return true
		}
	}
}

// snapshot returns diagnostics for every URI, plus the publish counters.
func (c *client) snapshot() (map[string][]Diagnostic, map[string]int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := make(map[string][]Diagnostic, len(c.diags))
	s := make(map[string]int64, len(c.seq))
	for k, v := range c.diags {
		d[k] = v
	}
	for k, v := range c.seq {
		s[k] = v
	}
	return d, s
}

func (c *client) alive() bool {
	select {
	case <-c.conn.done:
		return false
	default:
		return true
	}
}

// shutdown asks the server to exit, killing it if it doesn't.
func (c *client) shutdown() {
	if c.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.conn.call(ctx, "shutdown", nil, nil)
		cancel()
		_ = c.conn.notify("exit", nil)
		select {
		case <-c.conn.done:
		case <-time.After(time.Second):
		}
	}
	c.kill()
}

func (c *client) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.conn != nil {
		_ = c.conn.close()
	}
	c.closeLog()
}

func (c *client) closeLog() {
	if c.log != nil {
		_ = c.log.Close()
		c.log = nil
	}
}

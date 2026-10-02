// Package claudeagent adapts the official Claude CLI as a whole-turn agent
// runtime. Authentication remains entirely owned by that CLI; Larik never
// reads, stores, refreshes, or logs its OAuth credentials.
package claudeagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"larik/internal/llm"
)

const (
	maxOutputLine = 8 << 20
	maxStderr     = 64 << 10
	maxMCPBody    = 2 << 20
)

// SuggestedModels are rolling aliases documented by Claude Code's --model
// help. They follow the newest model in each family; the CLI remains the
// authority for whether an alias is available to a given account.
var SuggestedModels = []string{"sonnet", "opus", "fable"}

var requiredFlags = []string{
	"--print", "--tools", "--allowedTools", "--strict-mcp-config",
	"--mcp-config", "--permission-mode", "--input-format", "--output-format",
	"--include-partial-messages", "--no-session-persistence", "--model",
	"--effort", "--disable-slash-commands", "--no-chrome",
	"--prompt-suggestions", "--verbose", "--system-prompt",
	"--setting-sources",
}

// isolationFlags keep the user's own Claude Code setup (settings, hooks,
// plugins) out of the child, in order of preference; one is required.
// --restricted ignores settings files but keeps --mcp-config servers.
// --safe-mode, for CLIs without it, also turns off every MCP server in
// current releases, Larik's tool bridge included, so the model gets no
// tools there.
var isolationFlags = []string{"--restricted", "--safe-mode"}

type State string

const (
	StateReady        State = "ready"
	StateMissing      State = "missing"
	StateIncompatible State = "incompatible"
	StateLoggedOut    State = "logged_out"
	StateWrongAuth    State = "wrong_auth_source"
)

type Status struct {
	State, Path, Version, Detail string
	// MaxTurns reports whether this CLI accepts --max-turns. Newer releases
	// may omit it; the runtime then enforces the same ceiling from the stream.
	MaxTurns bool
	// Isolation is the isolationFlags entry this CLI supports.
	Isolation string
}

func (s Status) Ready() bool { return s.State == string(StateReady) }

// Runtime uses the process environment and PATH by default. The fields are
// seams for deterministic tests with fake Claude executables.
type Runtime struct {
	Executable string
	LookupPath func(string) (string, error)
	Environ    func() []string
}

var _ llm.AgentRuntime = (*Runtime)(nil)

func New() *Runtime { return &Runtime{} }

func (r *Runtime) executable() (string, error) {
	if r.Executable != "" {
		return r.Executable, nil
	}
	lookup := r.LookupPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	return lookup("claude")
}

// Check verifies the installed CLI contract and asks the CLI itself which
// credential source is active. It never opens a login flow.
func (r *Runtime) Check(ctx context.Context) Status {
	path, err := r.executable()
	if err != nil {
		return Status{State: string(StateMissing), Detail: "Claude CLI not found; install it, then run: claude auth login"}
	}
	help, err := commandOutput(ctx, path, "--help")
	if err != nil {
		return Status{State: string(StateIncompatible), Path: path, Detail: "could not inspect Claude CLI: " + err.Error()}
	}
	var missing []string
	for _, flag := range requiredFlags {
		if !bytes.Contains(help, []byte(flag)) {
			missing = append(missing, flag)
		}
	}
	isolation := ""
	for _, flag := range isolationFlags {
		if bytes.Contains(help, []byte(flag)) {
			isolation = flag
			break
		}
	}
	if isolation == "" {
		missing = append(missing, strings.Join(isolationFlags, " or "))
	}
	if len(missing) > 0 {
		return Status{State: string(StateIncompatible), Path: path, Detail: "Claude CLI is missing required flags: " + strings.Join(missing, ", ")}
	}
	hasMaxTurns := bytes.Contains(help, []byte("--max-turns"))
	version, _ := commandOutput(ctx, path, "--version")
	authJSON, err := commandOutput(ctx, path, "auth", "status", "--json")
	if err != nil {
		return Status{State: string(StateLoggedOut), Path: path, Version: strings.TrimSpace(string(version)), Detail: "Claude CLI is not signed in; run: claude auth login"}
	}
	var auth struct {
		LoggedIn    bool   `json:"loggedIn"`
		AuthMethod  string `json:"authMethod"`
		APIProvider string `json:"apiProvider"`
	}
	if json.Unmarshal(authJSON, &auth) != nil {
		return Status{State: string(StateIncompatible), Path: path, Version: strings.TrimSpace(string(version)), Detail: "Claude CLI returned malformed auth status"}
	}
	if !auth.LoggedIn {
		return Status{State: string(StateLoggedOut), Path: path, Version: strings.TrimSpace(string(version)), Detail: "Claude CLI is not signed in; run: claude auth login"}
	}
	method, provider := strings.ToLower(auth.AuthMethod), strings.ToLower(auth.APIProvider)
	if provider != "firstparty" || method == "api_key" || strings.Contains(method, "bedrock") || strings.Contains(method, "vertex") || strings.Contains(method, "foundry") {
		return Status{State: string(StateWrongAuth), Path: path, Version: strings.TrimSpace(string(version)), Detail: "Claude CLI is using an API key or cloud provider; sign in with a first-party Claude subscription using: claude auth login"}
	}
	return Status{State: string(StateReady), Path: path, Version: strings.TrimSpace(string(version)), Detail: "Claude CLI subscription authentication is ready", MaxTurns: hasMaxTurns, Isolation: isolation}
}

func commandOutput(ctx context.Context, path string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	// Status must see the user's real credential selection so an ambient API
	// key is reported as the wrong source instead of masquerading as logged out.
	// Only inference children receive the sanitized environment below.
	command.Env = os.Environ()
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	return output, nil
}

// Run starts one isolated Claude CLI process and its ephemeral MCP bridge.
func (r *Runtime) Run(ctx context.Context, request llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("claude-code-cli requires a model")
	}
	statusCtx, cancelStatus := context.WithTimeout(ctx, 15*time.Second)
	status := r.Check(statusCtx)
	cancelStatus()
	if !status.Ready() {
		return nil, errors.New(status.Detail)
	}

	runCtx, cancel := context.WithCancel(ctx)
	out := make(chan llm.AgentRuntimeEvent, 32)
	matcher := &callMatcher{}
	bridge, err := startBridge(runCtx, request.Tools, out, matcher)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start Claude tool bridge: %w", err)
	}

	_, input, err := encodeInput(request.Messages)
	if err != nil {
		cancel()
		bridge.Close(context.Background())
		return nil, err
	}
	system := request.System
	effort := request.Effort
	if effort == "" {
		effort = "high"
	}
	maxTurns := request.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 200
	}
	args := []string{
		"--print", status.Isolation, "--tools", "", "--allowedTools", strings.Join(bridge.toolNames, ","),
		"--strict-mcp-config", "--mcp-config", bridge.configPath,
		"--permission-mode", "dontAsk", "--input-format", "stream-json", "--output-format", "stream-json",
		"--include-partial-messages", "--no-session-persistence", "--disable-slash-commands", "--no-chrome",
		"--setting-sources", "",
		"--prompt-suggestions", "false", "--verbose", "--model", request.Model,
		"--effort", string(effort),
	}
	if status.MaxTurns {
		args = append(args, "--max-turns", strconv.Itoa(maxTurns))
	}
	if system != "" {
		args = append(args, "--system-prompt", system)
	}
	command := exec.CommandContext(runCtx, status.Path, args...)
	prepareCommand(command)
	command.Dir = request.Workspace
	if command.Dir == "" {
		command.Dir, _ = os.Getwd()
	}
	environ := os.Environ()
	if r.Environ != nil {
		environ = r.Environ()
	}
	command.Env = sanitizedEnv(environ)
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		bridge.Close(context.Background())
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		bridge.Close(context.Background())
		return nil, err
	}
	var stderr limitedBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		bridge.Close(context.Background())
		return nil, fmt.Errorf("start Claude CLI: %w", err)
	}

	go func() {
		defer close(out)
		defer bridge.Close(context.Background())
		defer cancel()
		inputErr := make(chan error, 1)
		go writeInput(runCtx, stdin, input, nil, out, matcher, inputErr)
		parseErr := parseOutput(runCtx, stdout, out, matcher, maxTurns)
		if parseErr != nil {
			// Do not wait for a malformed or oversized stream to exit by itself.
			// Closing the run context invokes the process-group cancellation hook.
			cancel()
			_ = command.Wait()
			_ = stdin.Close()
			send(ctx, out, llm.AgentRuntimeEvent{Err: parseErr})
			return
		}
		waitErr := command.Wait()
		_ = stdin.Close()
		select {
		case err := <-inputErr:
			if parseErr == nil && err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.ErrClosedPipe) {
				parseErr = fmt.Errorf("write Claude input: %w", err)
			}
		default:
		}
		if runCtx.Err() != nil {
			return
		}
		if waitErr != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				waitErr = fmt.Errorf("%w: %s", waitErr, detail)
			}
			send(runCtx, out, llm.AgentRuntimeEvent{Err: fmt.Errorf("Claude CLI failed: %w", waitErr)})
			return
		}
	}()
	return out, nil
}

func send(ctx context.Context, out chan<- llm.AgentRuntimeEvent, event llm.AgentRuntimeEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func sanitizedEnv(environment []string) []string {
	out := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		// The CLI's own OAuth/keychain path remains available. Every Anthropic
		// API override and supported cloud-provider selector is removed so this
		// provider cannot silently turn into metered API or third-party traffic.
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") ||
			strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") ||
			strings.HasPrefix(key, "AZURE_") || strings.HasPrefix(key, "CLOUD_ML_") {
			continue
		}
		out = append(out, item)
	}
	return out
}

type inputMessage struct {
	Type    string       `json:"type"`
	Message inputPayload `json:"message"`
}
type inputPayload struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func encodeInput(messages []llm.Message) (string, inputMessage, error) {
	var system []string
	var transcript strings.Builder
	var images []map[string]any
	for _, message := range messages {
		if message.Role == "system" {
			system = append(system, message.Text())
			continue
		}
		fmt.Fprintf(&transcript, "\n<message role=%q>", message.Role)
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockText:
				transcript.WriteString("\n" + block.Text)
			case llm.BlockToolUse:
				fmt.Fprintf(&transcript, "\n<tool_call id=%q name=%q>%s</tool_call>", block.ID, block.Name, block.Input)
			case llm.BlockToolResult:
				fmt.Fprintf(&transcript, "\n<tool_result id=%q error=%t>%s</tool_result>", block.ID, block.IsError, block.Content)
			case llm.BlockImage:
				images = append(images, cliImage(block))
			}
			for _, image := range block.Images {
				images = append(images, cliImage(image))
			}
		}
		transcript.WriteString("\n</message>\n")
	}
	content := any(strings.TrimSpace(transcript.String()))
	if len(images) > 0 {
		blocks := []map[string]any{{"type": "text", "text": content}}
		content = append(blocks, images...)
	}
	return strings.Join(system, "\n\n"), inputMessage{Type: "user", Message: inputPayload{Role: "user", Content: content}}, nil
}

func cliImage(image llm.Block) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MediaType, "data": image.Data}}
}

func writeInput(_ context.Context, writer io.WriteCloser, initial inputMessage, _ <-chan string, _ chan<- llm.AgentRuntimeEvent, _ *callMatcher, done chan<- error) {
	err := json.NewEncoder(writer).Encode(initial)
	_ = writer.Close()
	done <- err
}

type streamEnvelope struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Event   json.RawMessage `json:"event"`
	Message json.RawMessage `json:"message"`
	Usage   *usageJSON      `json:"usage"`
	IsError bool            `json:"is_error"`
	Result  string          `json:"result"`
	Errors  []string        `json:"errors"`
}
type usageJSON struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func parseOutput(ctx context.Context, reader io.Reader, out chan<- llm.AgentRuntimeEvent, matcher *callMatcher, maxTurns int) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxOutputLine)
	usageSent := false
	turns := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope streamEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return fmt.Errorf("malformed Claude stream JSON: %w", err)
		}
		switch envelope.Type {
		case "stream_event":
			var event struct {
				Type  string `json:"type"`
				Delta struct {
					Type, Text, Thinking string
				} `json:"delta"`
			}
			if json.Unmarshal(envelope.Event, &event) != nil {
				return errors.New("malformed Claude partial message")
			}
			switch event.Delta.Type {
			case "text_delta":
				send(ctx, out, llm.AgentRuntimeEvent{Text: event.Delta.Text})
			case "thinking_delta":
				send(ctx, out, llm.AgentRuntimeEvent{Thinking: event.Delta.Thinking})
			}
		case "assistant":
			turns++
			if maxTurns > 0 && turns > maxTurns {
				return fmt.Errorf("Claude runtime exceeded the Larik limit of %d model iterations", maxTurns)
			}
			message, calls, err := decodeAssistant(envelope.Message)
			if err != nil {
				return err
			}
			for _, call := range calls {
				matcher.add(call)
			}
			send(ctx, out, llm.AgentRuntimeEvent{Assistant: &message})
		case "result":
			if envelope.IsError || (envelope.Subtype != "" && envelope.Subtype != "success") {
				reason := strings.Join(envelope.Errors, "; ")
				if reason == "" {
					reason = envelope.Result
				}
				return fmt.Errorf("Claude runtime %s: %s", envelope.Subtype, reason)
			}
			if envelope.Usage != nil && !usageSent {
				usageSent = true
				u := envelope.Usage
				send(ctx, out, llm.AgentRuntimeEvent{Usage: &llm.Usage{
					Input: int(u.InputTokens), Output: int(u.OutputTokens),
					CacheRead: int(u.CacheReadInputTokens), CacheWrite: int(u.CacheCreationInputTokens),
				}})
			}
			send(ctx, out, llm.AgentRuntimeEvent{Done: true})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Claude stream: %w", err)
	}
	return nil
}

func decodeAssistant(raw json.RawMessage) (llm.Message, []llm.Block, error) {
	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type, Text, ID, Name string
			Input                json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return llm.Message{}, nil, fmt.Errorf("malformed Claude assistant message: %w", err)
	}
	var text strings.Builder
	var calls []llm.Block
	for _, block := range wire.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			name := strings.TrimPrefix(block.Name, "mcp__larik__")
			call := llm.Block{ID: string(block.ID), Name: name, Input: block.Input, Type: llm.BlockToolUse}
			calls = append(calls, call)
		}
	}
	blocks := []llm.Block{}
	if text.Len() > 0 {
		blocks = append(blocks, llm.TextBlock(text.String()))
	}
	blocks = append(blocks, calls...)
	return llm.Message{Role: llm.RoleAssistant, Blocks: blocks}, calls, nil
}

type callMatcher struct {
	mu    sync.Mutex
	queue []matchedCall
}

type matchedCall struct{ call llm.Block }

func (m *callMatcher) add(call llm.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, matchedCall{call: call})
}

func (m *callMatcher) take(name string, arguments json.RawMessage) (string, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, matched := range m.queue {
		call := matched.call
		if call.Name == name && jsonEqual(call.Input, arguments) {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return call.ID, true, false
		}
	}
	return "", false, false
}
func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

type bridgeServer struct {
	server     *http.Server
	listener   net.Listener
	configPath string
	toolNames  []string
	once       sync.Once
}

var callSequence atomic.Uint64

func startBridge(ctx context.Context, tools []llm.ToolSpec, out chan<- llm.AgentRuntimeEvent, matcher *callMatcher) (*bridgeServer, error) {
	if len(tools) == 0 {
		return &bridgeServer{}, nil
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	expected := "Bearer " + token
	server := mcp.NewServer(&mcp.Implementation{Name: "larik", Version: "1"}, nil)
	bridge := &bridgeServer{}
	var toolMu sync.Mutex
	for _, definition := range tools {
		definition := definition
		schema := definition.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		server.AddTool(&mcp.Tool{Name: definition.Name, Description: definition.Description, InputSchema: json.RawMessage(schema)},
			func(callCtx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				toolMu.Lock()
				defer toolMu.Unlock()
				arguments := request.Params.Arguments
				id, ok, _ := matcher.take(definition.Name, arguments)
				if !ok {
					id = string(fmt.Sprintf("claude-tool-%d", callSequence.Add(1)))
				}
				result := make(chan llm.Block, 1)
				runtimeRequest := llm.AgentRuntimeToolRequest{Call: llm.Block{ID: id, Name: definition.Name, Input: arguments, Type: llm.BlockToolUse}, Result: result}
				if !send(ctx, out, llm.AgentRuntimeEvent{Tool: &runtimeRequest}) {
					return nil, ctx.Err()
				}
				select {
				case <-callCtx.Done():
					return nil, callCtx.Err()
				case <-ctx.Done():
					return nil, ctx.Err()
				case resolved := <-result:
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: resolved.Content}}, IsError: resolved.IsError}, nil
				}
			})
		bridge.toolNames = append(bridge.toolNames, "mcp__larik__"+definition.Name)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true, MaxRequestBodyBytes: maxMCPBody})
	authenticated := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("Authorization")
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(response, request)
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	bridge.listener = listener
	bridge.server = &http.Server{Handler: authenticated, ReadHeaderTimeout: 5 * time.Second}
	config := map[string]any{"mcpServers": map[string]any{"larik": map[string]any{
		"type": "http", "url": "http://" + listener.Addr().String(), "headers": map[string]string{"Authorization": expected},
	}}}
	encoded, err := json.Marshal(config)
	if err != nil {
		listener.Close()
		return nil, err
	}
	file, err := os.CreateTemp("", "larik-claude-mcp-*.json")
	if err != nil {
		listener.Close()
		return nil, err
	}
	bridge.configPath = file.Name()
	if err := file.Chmod(0o600); err == nil {
		_, err = file.Write(encoded)
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		listener.Close()
		os.Remove(file.Name())
		return nil, errors.Join(err, closeErr)
	}
	go func() { _ = bridge.server.Serve(listener) }()
	return bridge, nil
}

func (b *bridgeServer) Close(ctx context.Context) {
	b.once.Do(func() {
		if b.server == nil {
			return
		}
		shutdown, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = b.server.Shutdown(shutdown)
		_ = b.listener.Close()
		_ = os.Remove(b.configPath)
	})
}

type limitedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxStderr - len(b.b)
	if remaining > 0 {
		b.b = append(b.b, p[:min(remaining, len(p))]...)
	}
	return len(p), nil
}
func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.b) }

// ConfigPermissions reports the file mode used for a bridge configuration.
// It is exported only to keep security-focused black-box tests independent of
// implementation details.
func ConfigPermissions(path string) (os.FileMode, error) {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

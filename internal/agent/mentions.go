package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// mention is an @path in a prompt, optionally with a #Lfrom-to line range.
type mention struct {
	path     string
	from, to int // 1-based and inclusive; zero when absent
}

// findMentions returns the @path mentions in text. A mention starts the
// text or follows whitespace, so e-mail addresses don't count; @"a b"
// quotes a path with spaces; trailing punctuation is not part of it.
func findMentions(text string) []mention {
	var out []mention
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text[:i]); !unicode.IsSpace(r) {
				continue
			}
		}
		rest := text[i+1:]
		var tok string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				continue
			}
			tok = rest[1 : end+1]
			i += end + 2
		} else {
			end := strings.IndexFunc(rest, unicode.IsSpace)
			if end < 0 {
				end = len(rest)
			}
			tok = strings.TrimRight(rest[:end], ".,;:!?)'\"")
			i += end
		}
		m := mention{path: tok}
		if p, lines, ok := strings.Cut(tok, "#L"); ok {
			a, b, _ := strings.Cut(lines, "-")
			from, err1 := strconv.Atoi(a)
			to, err2 := strconv.Atoi(b)
			if b == "" {
				to, err2 = from, nil
			}
			if err1 == nil && err2 == nil && from > 0 && to >= from {
				m = mention{path: p, from: from, to: to}
			}
		}
		if m.path != "" {
			out = append(out, m)
		}
	}
	return out
}

// imageTypes are the image formats every vision-capable provider accepts.
var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

const (
	// MaxImageBytes is the largest image a prompt may attach.
	MaxImageBytes = 5 << 20
	maxDirEntries = 200
)

// resolveMentions reads the files, images and directories a prompt
// mentions into attachment blocks. Paths that don't exist are prose
// ("@alice"), not errors. Typing a mention is consent to read it, so only
// deny rules stop one.
func (a *Agent) resolveMentions(ctx context.Context, text string, emit func(Event)) []llm.Block {
	var blocks []llm.Block
	seen := map[string]bool{}
	notice := func(format string, args ...any) {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf(format, args...)})
	}
	for _, mn := range findMentions(text) {
		abs := a.env.Abs(expandHome(mn.path))
		fi, err := os.Stat(abs)
		if err != nil && !seen[mn.path] {
			// Not a file: maybe an MCP resource, @server:uri.
			seen[mn.path] = true
			if res, ok := a.mentionResource(ctx, mn.path, notice); ok {
				blocks = append(blocks, res...)
			}
			continue
		}
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		name := a.displayPath(abs)
		if reason, denied := a.userDenied("read", map[string]any{"path": abs}); denied {
			notice("not attaching @%s: %s", name, reason)
			continue
		}
		switch {
		case fi.IsDir():
			b, n, err := dirListing(abs, name)
			if err != nil {
				notice("couldn't attach @%s: %v", name, err)
				continue
			}
			blocks = append(blocks, b)
			notice("attached @%s (%d entries)", name, n)
		case imageTypes[strings.ToLower(filepath.Ext(abs))] != "":
			if fi.Size() > MaxImageBytes {
				notice("not attaching @%s: images are limited to %d MB", name, MaxImageBytes>>20)
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				notice("couldn't attach @%s: %v", name, err)
				continue
			}
			blocks = append(blocks,
				llm.Block{Type: llm.BlockText, Text: fmt.Sprintf("<image path=%q/>", name), Attachment: name},
				llm.Block{Type: llm.BlockImage, MediaType: imageTypes[strings.ToLower(filepath.Ext(abs))], Data: base64.StdEncoding.EncodeToString(data), Attachment: name})
			notice("attached @%s (image, %d KB)", name, (len(data)+1023)/1024)
		default:
			in := map[string]any{"path": abs}
			attr := ""
			if mn.from > 0 {
				in["offset"], in["limit"] = mn.from, mn.to-mn.from+1
				attr = fmt.Sprintf(" lines=\"%d-%d\"", mn.from, mn.to)
			}
			raw, _ := json.Marshal(in)
			// The read tool formats, caps and marks the file read, so the
			// model can edit it without reading it again.
			res := tools.Read{}.Run(ctx, a.env, raw)
			if res.IsError {
				notice("couldn't attach @%s: %s", name, res.Content)
				continue
			}
			blocks = append(blocks, llm.Block{Type: llm.BlockText, Attachment: name,
				Text: fmt.Sprintf("<file path=%q%s>\n%s</file>", name, attr, res.Content)})
			notice("attached @%s (%s)", name, plural(strings.Count(res.Content, "\n"), "line"))
		}
	}
	return blocks
}

// Shell runs a command the user typed (a "!" prompt) and queues its output
// for the next prompt. It runs like the bash tool, in the sandbox when
// there is one, but without asking: only deny rules stop it.
func (a *Agent) Shell(ctx context.Context, command string) tools.Result {
	raw, _ := json.Marshal(map[string]string{"command": command})
	if reason, denied := a.userDenied("bash", map[string]any{"command": command}); denied {
		return tools.Result{Content: reason, IsError: true}
	}
	res := tools.Bash{}.Run(ctx, a.env, raw)
	out := strings.TrimRight(res.Content, "\n") // already capped by the tool
	a.mu.Lock()
	a.pending = append(a.pending, llm.Block{Type: llm.BlockText, Attachment: "!" + command,
		Text: fmt.Sprintf("The user ran a shell command:\n<bash-input>%s</bash-input>\n<bash-output>%s</bash-output>", command, out)})
	a.mu.Unlock()
	return res
}

// userDenied applies deny rules to something the user asked for directly.
// It asks as a read-only call, so the mode and allow rules don't matter.
func (a *Agent) userDenied(tool string, in map[string]any) (string, bool) {
	if a.opts.Perms == nil {
		return "", false
	}
	raw, _ := json.Marshal(in)
	d, reason := a.opts.Perms.Decide(permission.Call{Tool: tool, ReadOnly: true, Input: raw})
	return reason, d == permission.Deny
}

// displayPath is path relative to the working directory when inside it.
func (a *Agent) displayPath(path string) string {
	if rel, err := filepath.Rel(a.env.Cwd, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return filepath.ToSlash(rel)
	}
	return path
}

func dirListing(abs, name string) (llm.Block, int, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return llm.Block{}, 0, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<directory path=%q>\n", name)
	for i, e := range entries {
		if i == maxDirEntries {
			fmt.Fprintf(&b, "... (%d more)\n", len(entries)-i)
			break
		}
		b.WriteString(e.Name())
		if e.IsDir() {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
	}
	b.WriteString("</directory>")
	return llm.Block{Type: llm.BlockText, Text: b.String(), Attachment: name}, len(entries), nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// MCPContent is what the agent needs from MCP servers besides their
// tools: resources for @server:uri mentions, and prompts run as
// /mcp__server__prompt.
type MCPContent interface {
	IsServer(name string) bool
	ReadResource(ctx context.Context, server, uri string) ([]llm.Block, error)
	ExpandPrompt(ctx context.Context, line string) (text string, ok bool, err error)
}

// mentionResource attaches @server:uri when server is a connected MCP
// server. ok is false when the mention isn't one, so it stays prose.
func (a *Agent) mentionResource(ctx context.Context, path string, notice func(string, ...any)) (blocks []llm.Block, ok bool) {
	server, uri, found := strings.Cut(path, ":")
	if !found || uri == "" || a.opts.MCP == nil || !a.opts.MCP.IsServer(server) {
		return nil, false
	}
	blocks, err := a.opts.MCP.ReadResource(ctx, server, uri)
	if err != nil {
		notice("couldn't attach @%s: %v", path, err)
		return nil, true
	}
	notice("attached @%s (MCP resource)", path)
	return blocks, true
}

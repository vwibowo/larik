package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/clipboard"
	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/session"
	"larik/internal/tools"
)

// Composer features beyond plain text: @file mentions, prompt history,
// "!" shell commands, editing in $EDITOR and pasting file paths.

const (
	maxIndexedFiles = 20_000
	maxHistory      = 500
	mentionRows     = 50
	indexMaxAge     = 30 * time.Second
)

type (
	filesIndexedMsg struct{ files []string }
	shellDoneMsg    struct {
		command string
		res     tools.Result
	}
	editorDoneMsg struct {
		text string
		err  error
	}
)

// syncComposer updates everything that follows the input text: the
// command palette, the mention popup and the shell-mode prompt.
func (m *model) syncComposer() tea.Cmd {
	m.syncPalette()
	if m.shellMode() {
		m.input.Prompt = "! "
	} else {
		m.input.Prompt = "› "
	}
	if m.palette != nil {
		m.mention = nil
		return nil
	}
	return m.syncMention()
}

func (m *model) shellMode() bool { return strings.HasPrefix(m.input.Value(), "!") }

// mentionToken is the @word the cursor is at the end of.
type mentionToken struct {
	line, start, end int // rune columns within the line
	query            string
}

func (t mentionToken) key() string { return fmt.Sprintf("%d:%d", t.line, t.start) }

func (m *model) activeMention() (mentionToken, bool) {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row >= len(lines) {
		return mentionToken{}, false
	}
	runes := []rune(lines[row])
	col := min(m.input.Column(), len(runes))
	start := col
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	if start == col || runes[start] != '@' {
		return mentionToken{}, false
	}
	return mentionToken{line: row, start: start, end: col, query: string(runes[start+1 : col])}, true
}

// syncMention opens, updates or closes the @ popup, and indexes the
// project's files when it opens.
func (m *model) syncMention() tea.Cmd {
	tok, ok := m.activeMention()
	if !ok || strings.HasPrefix(tok.query, `"`) {
		m.mention, m.mentionHidden = nil, ""
		return nil
	}
	if m.mentionHidden == tok.key() { // dismissed with esc while typing this one
		return nil
	}
	m.mentionHidden = ""
	var cmd tea.Cmd
	if m.mention == nil && !m.indexing && (m.files == nil || time.Since(m.filesAt) > indexMaxAge) {
		m.indexing = true
		cmd = indexFiles(m.agent.Cwd())
	}
	if m.mention == nil || m.mentionTok.query != tok.query {
		p := &picker{}
		for _, f := range rankFiles(m.files, tok.query, mentionRows) {
			dir, base := path.Split(strings.TrimSuffix(f, "/"))
			if strings.HasSuffix(f, "/") {
				base += "/"
			}
			p.items = append(p.items, pickItem{label: base, detail: dir, value: f})
		}
		for _, r := range rankResources(m.resources, tok.query, mentionRows) {
			label := r.Name
			if label == "" {
				label = r.URI
			}
			p.items = append(p.items, pickItem{section: "MCP resources", label: label, detail: r.Server + " · " + r.URI, value: r.Ref()})
		}
		p.home()
		m.mention = p
	}
	m.mentionTok = tok
	return tea.Batch(cmd, m.loadResources())
}

// resourcesLoadedMsg carries the MCP resources for the @ popup.
type resourcesLoadedMsg struct{ resources []mcp.Resource }

// loadResources lists MCP resources for the @ popup, at most every
// indexMaxAge, when a server offers any.
func (m *model) loadResources() tea.Cmd {
	mgr := m.opts.MCP
	if mgr == nil || m.loadingResources || time.Since(m.resourcesAt) < indexMaxAge {
		return nil
	}
	m.loadingResources = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rs, _ := mgr.Resources(ctx, "")
		return resourcesLoadedMsg{rs}
	}
}

// rankResources keeps the resources whose server:uri or name contains the
// query, in their listed order.
func rankResources(rs []mcp.Resource, query string, n int) []mcp.Resource {
	q := strings.ToLower(query)
	var out []mcp.Resource
	for _, r := range rs {
		if len(out) == n {
			break
		}
		if q == "" || strings.Contains(strings.ToLower(r.Ref()), q) || strings.Contains(strings.ToLower(r.Name), q) {
			out = append(out, r)
		}
	}
	return out
}

// handleMentionKey handles keys while the @ popup shows; ok is false for
// keys that belong to the input.
func (m *model) handleMentionKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	p := m.mention
	switch msg.String() {
	case "up", "ctrl+p", "down", "ctrl+n", "pgup", "pgdown":
		p.handleKey(msg)
		return nil, true
	case "esc":
		m.mentionHidden = m.mentionTok.key()
		m.mention = nil
		return nil, true
	case "tab", "enter":
		sel, has := p.selected()
		if !has {
			return nil, false
		}
		m.insertMention(sel.value.(string))
		return m.syncComposer(), true
	}
	return nil, false
}

// insertMention replaces the @token at the cursor with a completed path.
// A directory stays open for picking inside it; a file ends the mention.
func (m *model) insertMention(p string) {
	tok := m.mentionTok
	lines := strings.Split(m.input.Value(), "\n")
	runes := []rune(lines[tok.line])
	ins := "@" + p
	if strings.ContainsAny(p, " \t") {
		ins = `@"` + p + `"`
	}
	if !strings.HasSuffix(p, "/") {
		ins += " "
	}
	lines[tok.line] = string(runes[:tok.start]) + ins + string(runes[tok.end:])
	m.input.SetValue(strings.Join(lines, "\n"))
	m.input.MoveToBegin()
	for i := 0; m.input.Line() < tok.line && i < 10_000; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(tok.start + len([]rune(ins)))
	m.mention = nil
}

func (m *model) mentionView() string {
	p := m.mention
	w := max(m.width-6, 20)
	p.height = max(min(8, m.availablePanelRows()-3), 1)
	body := p.view(m.st, w)
	switch {
	case m.files == nil:
		body = m.st.dim.Render("  looking for files…")
	case len(p.items) == 0:
		body = m.st.dim.Render("  no matching files")
	}
	hint := "↑/↓ move · tab or enter insert · esc close"
	return m.st.modal.Width(max(m.width-2, 10)).Render(body + "\n" + m.st.dim.Render(hint))
}

// indexFiles lists the project's files, preferring git so .gitignore is
// honored, and adds each directory as "dir/".
func indexFiles(cwd string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var files []string
		out, err := exec.CommandContext(ctx, "git", "-C", cwd, "ls-files", "-co", "--exclude-standard", "-z").Output()
		if err == nil {
			for _, f := range strings.Split(string(out), "\x00") {
				if f != "" && len(files) < maxIndexedFiles {
					files = append(files, f)
				}
			}
		} else {
			_ = tools.WalkFiles(ctx, cwd, func(p string) error {
				if len(files) >= maxIndexedFiles {
					return fs.SkipAll
				}
				if rel, err := filepath.Rel(cwd, p); err == nil {
					files = append(files, filepath.ToSlash(rel))
				}
				return nil
			})
		}
		dirs := map[string]bool{}
		for _, f := range files {
			for d := path.Dir(f); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
				dirs[d] = true
			}
		}
		for d := range dirs {
			files = append(files, d+"/")
		}
		if files == nil {
			files = []string{} // indexed, but empty
		}
		return filesIndexedMsg{files}
	}
}

// rankFiles orders paths for a typed query: path prefix (for drilling into
// a directory), then name prefix, name substring, path substring, and
// in-order letters; shorter paths first within each. An empty query lists
// the top level.
func rankFiles(files []string, query string, n int) []string {
	q := strings.ToLower(query)
	type hit struct {
		p     string
		score int
	}
	var hits []hit
	for _, f := range files {
		lp := strings.ToLower(f)
		if lp == q {
			continue // the directory already typed; list what's in it
		}
		base := path.Base(strings.TrimSuffix(lp, "/"))
		score := -1
		switch {
		case q == "":
			if strings.Count(strings.TrimSuffix(f, "/"), "/") == 0 {
				score = 0
			}
		case strings.HasPrefix(lp, q):
			score = 0
		case strings.HasPrefix(base, q):
			score = 1
		case strings.Contains(base, q):
			score = 2
		case strings.Contains(lp, q):
			score = 3
		case subsequence(lp, q):
			score = 4
		}
		if score >= 0 {
			hits = append(hits, hit{f, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if len(a.p) != len(b.p) {
			return len(a.p) < len(b.p)
		}
		return a.p < b.p
	})
	out := make([]string, 0, min(n, len(hits)))
	for _, h := range hits[:min(n, len(hits))] {
		out = append(out, h.p)
	}
	return out
}

func subsequence(s, sub string) bool {
	for _, r := range sub {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// Prompt history.

type historyEntry struct {
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// historyPath is per project, beside (not in) the session directory,
// which treats every .jsonl file as a session.
func (m *model) historyPath() string {
	c := m.opts.Config
	if c == nil || c.DataDir == "" {
		return ""
	}
	name := filepath.Base(session.Dir(c.DataDir, m.agent.Cwd()))
	return filepath.Join(c.DataDir, "history", name+".jsonl")
}

func (m *model) loadHistory() {
	p := m.historyPath()
	if p == "" {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var e historyEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Text != "" {
			all = append(all, e.Text)
		}
	}
	m.history = all[max(len(all)-maxHistory, 0):]
	if len(all) > 2*maxHistory { // compact now and then
		var b strings.Builder
		for _, t := range m.history {
			line, _ := json.Marshal(historyEntry{Text: t})
			b.Write(append(line, '\n'))
		}
		_ = os.WriteFile(p, []byte(b.String()), 0o600)
	}
}

// recordHistory remembers a submitted input, skipping repeats.
func (m *model) recordHistory(text string) {
	m.histIdx, m.histDraft = -1, ""
	if text == "" || (len(m.history) > 0 && m.history[len(m.history)-1] == text) {
		return
	}
	m.history = append(m.history, text)
	if len(m.history) > maxHistory {
		m.history = m.history[len(m.history)-maxHistory:]
	}
	p := m.historyPath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	line, _ := json.Marshal(historyEntry{Text: text, Time: time.Now()})
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// recallHistory steps through history: dir -1 is older, +1 newer. Past
// the newest entry it restores the draft. It reports whether it moved.
func (m *model) recallHistory(dir int) bool {
	switch {
	case dir < 0 && m.histIdx < 0:
		if len(m.history) == 0 {
			return false
		}
		m.histDraft, m.histIdx = m.input.Value(), len(m.history)-1
	case dir < 0 && m.histIdx > 0:
		m.histIdx--
	case dir < 0:
		return false
	case m.histIdx < 0:
		return false
	case m.histIdx < len(m.history)-1:
		m.histIdx++
	default:
		m.input.SetValue(m.histDraft)
		m.histIdx, m.histDraft = -1, ""
		return true
	}
	m.input.SetValue(m.history[m.histIdx])
	return true
}

func (m *model) openHistoryPicker() {
	p := &picker{filterable: true}
	for i := len(m.history) - 1; i >= 0; i-- {
		p.items = append(p.items, pickItem{label: oneLine(m.history[i], 200), value: m.history[i]})
	}
	p.home()
	m.histPick = p
}

func (m *model) handleHistoryPickKey(msg tea.KeyPressMsg) tea.Cmd {
	if k := msg.String(); k == "esc" || k == "ctrl+c" || k == "ctrl+r" {
		m.histPick = nil
		return nil
	}
	if m.histPick.handleKey(msg) {
		if it, ok := m.histPick.selected(); ok {
			m.input.SetValue(it.value.(string))
			m.histIdx = -1
		}
		m.histPick = nil
		return m.syncComposer()
	}
	return nil
}

func (m *model) historyPickerView() string {
	p := m.histPick
	w := max(m.width-6, 20)
	p.height = max(min(12, m.availablePanelRows()-5), 1)
	header := spread(m.st.accent.Render("Prompt history"), m.st.dim.Render("this project, newest first"), w)
	body := p.view(m.st, w)
	if len(p.items) == 0 {
		body = m.st.dim.Render("  nothing yet")
	}
	hint := m.st.dim.Render("↑/↓ move · type to filter · enter use · esc close")
	return m.st.modal.Width(max(m.width-2, 10)).Render(header + "\n" + p.filterLine(m.st, "search prompts…") + "\n" + body + "\n" + hint)
}

// Shell mode.

// runShell runs a "!" command. Its output is shown here and goes to the
// model with the next prompt.
func (m *model) runShell(command string) tea.Cmd {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.shellCancel = cancel
	m.busyLabel = "Running !" + oneLine(command, 40) + "…"
	a := m.agent
	run := func() tea.Msg { return shellDoneMsg{command, a.Shell(ctx, command)} }
	return tea.Sequence(
		m.println("\n"+m.st.warn.Render("! ")+m.st.user.Render(indentAfterFirst(command, "  "))),
		tea.Batch(run, m.spin.Tick),
	)
}

func (m *model) shellDone(msg shellDoneMsg) tea.Cmd {
	if m.shellCancel != nil {
		m.shellCancel()
	}
	m.shellCancel, m.busyLabel = nil, ""
	out := strings.TrimRight(msg.res.Content, "\n")
	if out == "" {
		out = "(no output)"
	}
	style := m.st.dim
	if msg.res.IsError {
		style = m.st.err
	}
	cmds := []tea.Cmd{m.println(prefixLines(style.Render(truncateLines(out, m.lines(12))), "  ⎿ ", "    "))}
	if !strings.HasPrefix(out, "denied by rule") { // a denied command isn't queued
		cmds = append(cmds, m.println(m.st.dim.Render("  output will be sent with your next prompt")))
	}
	if next := m.nextQueued(); next != nil {
		cmds = append(cmds, next)
	}
	return tea.Sequence(cmds...)
}

// External editor.

func (m *model) openEditor() tea.Cmd {
	f, err := os.CreateTemp("", "larik-prompt-*.md")
	if err != nil {
		return m.println(m.st.err.Render("couldn't open an editor: " + err.Error()))
	}
	name := f.Name()
	_, err = f.WriteString(m.input.Value())
	f.Close()
	if err != nil {
		os.Remove(name)
		return m.println(m.st.err.Render("couldn't open an editor: " + err.Error()))
	}
	args := strings.Fields(os.Getenv("VISUAL")) // e.g. "code --wait"
	if len(args) == 0 {
		args = strings.Fields(os.Getenv("EDITOR"))
	}
	if len(args) == 0 {
		args = []string{"vi"}
	}
	cmd := exec.Command(args[0], append(args[1:], name)...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		data, rerr := os.ReadFile(name)
		os.Remove(name)
		if err == nil {
			err = rerr
		}
		return editorDoneMsg{text: strings.TrimRight(string(data), "\n"), err: err}
	})
}

// Paste.

// pastedPath recognizes a single file path pasted or dropped into the
// terminal, which quotes or backslash-escapes it. Only absolute and ~/
// paths count, so pasted prose stays prose.
func (m *model) pastedPath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\n\r") {
		return "", false
	}
	s = strings.TrimPrefix(s, "file://")
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	} else if strings.Contains(s, `\`) {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		s = b.String()
	}
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, s[2:])
		}
	}
	if !filepath.IsAbs(s) {
		return "", false
	}
	if _, err := os.Stat(s); err != nil {
		return "", false
	}
	if rel, err := filepath.Rel(m.agent.Cwd(), s); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		s = filepath.ToSlash(rel)
	}
	return s, true
}

func (m *model) pasteMention(p string) {
	if strings.ContainsAny(p, " \t") {
		p = `"` + p + `"`
	}
	lead := ""
	if lines := strings.Split(m.input.Value(), "\n"); m.input.Line() < len(lines) {
		runes, col := []rune(lines[m.input.Line()]), m.input.Column()
		if col > 0 && col <= len(runes) && !unicode.IsSpace(runes[col-1]) {
			lead = " "
		}
	}
	m.input.InsertString(lead + "@" + p + " ")
}

// Image paste.

type imagePastedMsg struct {
	path string
	err  error
}

// pastedImageAge is how long pasted images are kept. The prompt carries
// the image itself once sent, so the file only has to outlive the draft.
const pastedImageAge = 7 * 24 * time.Hour

// pasteImage reads an image from the system clipboard (ctrl+v), saves it
// under the data directory and mentions it in the prompt, so it is
// attached like any @image.
func (m *model) pasteImage() tea.Cmd {
	if m.opts.Config == nil || m.opts.Config.DataDir == "" {
		return m.println(m.st.err.Render("can't paste images: no data directory"))
	}
	dir := filepath.Join(m.opts.Config.DataDir, "pastes")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		data, err := clipboard.ReadImage(ctx)
		if err != nil {
			return imagePastedMsg{err: err}
		}
		data, ext, err := clipboard.Fit(data, agent.MaxImageBytes)
		if err != nil {
			return imagePastedMsg{err: err}
		}
		path, err := clipboard.Save(dir, data, ext, func(fi os.FileInfo) bool { return time.Since(fi.ModTime()) < pastedImageAge })
		return imagePastedMsg{path: path, err: err}
	}
}

func (m *model) imagePasted(msg imagePastedMsg) tea.Cmd {
	if msg.err != nil {
		if errors.Is(msg.err, clipboard.ErrNoImage) {
			return m.println(m.st.dim.Render("no image on the clipboard (ctrl+v pastes images; paste text as usual)"))
		}
		return m.println(m.st.err.Render("couldn't paste the image: " + msg.err.Error()))
	}
	m.pasteMention(tildePath(msg.path))
	return m.syncComposer()
}

// Copying replies.

// lastReply is the text of the last assistant message in history.
func lastReply(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == llm.RoleAssistant {
			if t := strings.TrimSpace(history[i].Text()); t != "" {
				return t
			}
		}
	}
	return ""
}

// copyReply puts the last reply, as Markdown, on the clipboard: with the
// platform's tool, and through the terminal (OSC 52), which also reaches
// a local clipboard over SSH.
func (m *model) copyReply() tea.Cmd {
	if m.lastReply == "" {
		return m.println(m.st.dim.Render("no reply to copy yet"))
	}
	return m.copyText(m.lastReply, "the last reply")
}

// copyText puts text on the clipboard the way copyReply does; what names
// it in the confirmation, e.g. "the last reply".
func (m *model) copyText(text, what string) tea.Cmd {
	what += " (" + plural(strings.Count(text, "\n")+1, "line") + ")"
	native := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := clipboard.WriteText(ctx, text); err != nil {
			return outputMsg(m.st.dim.Render("sent " + what + " to the terminal's clipboard; if nothing was copied, the terminal doesn't allow it (OSC 52)"))
		}
		return outputMsg(m.st.dim.Render("✓ copied " + what))
	}
	return tea.Batch(tea.SetClipboard(text), native)
}

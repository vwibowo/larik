package desktop

import (
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/llm"
	"larik/internal/session"
)

func TestConversationIndex(t *testing.T) {
	dir := t.TempDir()
	s, err := session.Create(dir, session.Meta{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(llm.UserText("Why does sign-in loop?"), nil)
	s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		llm.TextBlock("The OAuth callback never checks the state parameter, so the second redirect starts over."),
		{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: []byte(`{"command":"grep -rn zebracorn"}`)},
	}}, nil)
	s.AppendMessage(llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockToolResult, ID: "t1", Content: "auth.go:12: zebracorn"},
		{Type: llm.BlockText, Text: "contents of notes.md: quokka", Attachment: "notes.md"},
	}}, nil)
	var x convIndex
	x.refresh([]string{s.Path})
	find := func(q string) string {
		t.Helper()
		return x.search(strings.Fields(q))[s.Path]
	}
	if got := find("oauth state"); !strings.Contains(got, "OAuth callback never checks the state parameter") {
		t.Fatalf("oauth state: %q", got)
	}
	if got := find("sign-in loop"); got != "Why does sign-in loop?" {
		t.Fatalf("a prompt: %q", got)
	}
	// Tool output and attached files aren't what was said; every word
	// must be.
	for _, q := range []string{"zebracorn", "quokka", "oauth zebracorn"} {
		if got := find(q); got != "" {
			t.Errorf("%s: %q", q, got)
		}
	}

	// It reads on from where it was.
	s.AppendMessage(llm.UserText("Now the logout button"), nil)
	read := x.files[s.Path].read
	x.refresh([]string{s.Path})
	if find("logout") == "" || len(x.files[s.Path].msgs) != 3 || x.files[s.Path].read <= read {
		t.Fatalf("after more was said: %q, %d messages", x.files[s.Path].msgs, len(x.files[s.Path].msgs))
	}
	s.Close()

	// A transcript rewritten shorter is read again from its start.
	data, _ := os.ReadFile(s.Path)
	first := strings.SplitAfterN(string(data), "\n", 3)
	if err := os.WriteFile(s.Path, []byte(first[0]+first[1]), 0o600); err != nil {
		t.Fatal(err)
	}
	x.refresh([]string{s.Path})
	if find("logout") != "" || find("sign-in") == "" {
		t.Fatalf("rewritten: %q", x.files[s.Path].msgs)
	}
	// One no longer listed is forgotten.
	x.refresh(nil)
	if len(x.files) != 0 {
		t.Fatal("kept a transcript no longer listed")
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("lorem ipsum ", 20) + "the needle\nis here " + strings.Repeat("dolor sit ", 20)
	got := snippet(long, strings.ToLower(long), strings.Index(long, "needle"), len("needle"))
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "the needle is here") {
		t.Fatalf("snippet: %q", got)
	}
	// Cut at a rune's edge, never inside it.
	accents := strings.Repeat("é", 40) + "needle"
	if got := snippet(accents, accents, strings.Index(accents, "needle"), 6); !strings.HasSuffix(got, "needle") || !strings.HasPrefix(got, "…é") {
		t.Fatalf("accents: %q", got)
	}
}

func TestSearchFindsWhatWasSaid(t *testing.T) {
	data, repo := fixture(t)
	s, err := session.Create(session.Dir(data, repo), session.Meta{Cwd: repo})
	if err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(llm.UserText("Fix sign-in"), nil)
	s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("The parser of the callback URL dropped the state parameter.")}}, nil)
	s.Close()
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("Search sessions (" + keyLabel(false, "K") + ")"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	tt.Type("parser")
	tt.Frame()
	mu.Unlock()
	frameUntil(t, mu, tt, "the conversation's match", func() bool { return tt.HasText("Fix sign-in") })

	mu.Lock()
	defer mu.Unlock()
	found := a.matches()
	if len(found) != 2 || found[0].Title != "Refactor the parser and run the tests" || found[1].Path != s.Path {
		t.Fatalf("names first, then what was said: %v", found)
	}
	if !tt.HasText("The parser of the callback URL dropped the state parameter.") {
		t.Fatalf("no passage: %q", tt.Texts())
	}
	// Two letters would find nearly everything; they search names only.
	tt.Key(0, ui.KeyEscape)
	tt.Type("pa")
	tt.Frame()
	if a.convHits != nil || a.saidIn(s.Path) != "" {
		t.Fatalf("searched conversations for %q", a.query)
	}
}

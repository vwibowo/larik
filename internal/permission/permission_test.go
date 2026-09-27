package permission

import (
	"encoding/json"
	"testing"
)

func call(tool string, readOnly bool, input string) Call {
	return Call{Tool: tool, ReadOnly: readOnly, Input: json.RawMessage(input)}
}

func TestDecide(t *testing.T) {
	rules := Rules{
		Allow: []string{"bash(go test*)", "edit(docs/**)"},
		Deny:  []string{"bash(rm -rf*)", "read(.env)", "mcp__prod-db"},
	}
	cases := []struct {
		name string
		mode Mode
		call Call
		want Decision
	}{
		{"read allowed", ModeDefault, call("read", true, `{"path":"main.go"}`), Allow},
		{"deny beats read-only", ModeDefault, call("read", true, `{"path":".env"}`), Deny},
		{"deny beats yolo", ModeYolo, call("bash", false, `{"command":"rm -rf /"}`), Deny},
		{"allow rule bash", ModeDefault, call("bash", false, `{"command":"go test ./..."}`), Allow},
		{"unknown bash asks", ModeDefault, call("bash", false, `{"command":"make deploy"}`), Ask},
		{"safe command", ModeDefault, call("bash", false, `{"command":"git status"}`), Allow},
		{"safe command chained", ModeDefault, call("bash", false, `{"command":"git status && rm x"}`), Ask},
		{"edit asks by default", ModeDefault, call("edit", false, `{"path":"main.go"}`), Ask},
		{"edit allow rule", ModeDefault, call("edit", false, `{"path":"docs/a/b.md"}`), Allow},
		{"accept-edits inside", ModeAcceptEdits, call("write", false, `{"path":"x/y.go"}`), Allow},
		{"accept-edits outside", ModeAcceptEdits, call("write", false, `{"path":"/etc/hosts"}`), Ask},
		{"accept-edits dotdot", ModeAcceptEdits, call("write", false, `{"path":"../other/y.go"}`), Ask},
		{"plan denies writes", ModePlan, call("edit", false, `{"path":"main.go"}`), Deny},
		{"plan allows reads", ModePlan, call("grep", true, `{"pattern":"x"}`), Allow},
		{"yolo", ModeYolo, call("bash", false, `{"command":"make deploy"}`), Allow},
		{"mcp asks by default", ModeDefault, call("mcp__jira__create", false, `{}`), Ask},
		{"mcp server deny", ModeYolo, call("mcp__prod-db__drop", false, `{}`), Deny},
		{"mcp read-only auto", ModeDefault, call("mcp__jira__get", true, `{}`), Allow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewChecker(tc.mode, rules, "/work/proj")
			if got, _ := c.Decide(tc.call); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMCPRules(t *testing.T) {
	c := NewChecker(ModeDefault, Rules{Allow: []string{"mcp__github", "mcp__docs__search"}}, "/w")
	cases := map[string]Decision{
		"mcp__github__create_issue": Allow, // server-wide rule
		"mcp__docs__search":         Allow, // exact rule
		"mcp__docs__delete":         Ask,
		"mcp__githubx__read":        Ask, // prefix must end at the server boundary
	}
	for tool, want := range cases {
		if got, _ := c.Decide(call(tool, false, `{}`)); got != want {
			t.Errorf("%s: got %v want %v", tool, got, want)
		}
	}
}

func TestSuggestRule(t *testing.T) {
	cases := map[string]string{
		`{"command":"git commit -m x"}`: "bash(git commit*)",
		`{"command":"npm run build"}`:   "bash(npm run*)",
		`{"command":"ls -la"}`:          "bash(ls*)",
		`{"command":"git -C x status"}`: "bash(git*)",
	}
	for in, want := range cases {
		if got := SuggestRule("bash", json.RawMessage(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
	if got := SuggestRule("edit", nil); got != "edit" {
		t.Errorf("edit: got %q", got)
	}
}

func TestWildcard(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"go test*", "go test ./...", true},
		{"go test*", "go vet", false},
		{"*--force*", "git push --force origin", true},
		{"exact", "exact", true},
		{"exact", "exactly", false},
	}
	for _, c := range cases {
		if got := wildcard(c.p, c.s); got != c.want {
			t.Errorf("wildcard(%q,%q)=%v", c.p, c.s, got)
		}
	}
}

func TestSandboxedBash(t *testing.T) {
	c := NewChecker(ModeDefault, Rules{Deny: []string{"bash(rm -rf*)"}}, "/w")
	c.SetSandboxed(true)
	cases := []struct {
		input string
		want  Decision
	}{
		{`{"command":"npm test"}`, Allow}, // confined: no prompt
		{`{"command":"npm test","sandbox":true}`, Allow},
		{`{"command":"npm install","sandbox":false}`, Ask},  // escaping asks
		{`{"command":"git status","sandbox":false}`, Allow}, // safe list still applies
		{`{"command":"rm -rf /"}`, Deny},                    // deny rules still win
	}
	for _, tc := range cases {
		if got, _ := c.Decide(call("bash", false, tc.input)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.input, got, tc.want)
		}
	}
	c.SetMode(ModePlan)
	if got, _ := c.Decide(call("bash", false, `{"command":"npm test"}`)); got != Deny {
		t.Error("plan mode stays read-only even with the sandbox")
	}
	c.SetMode(ModeDefault)
	c.SetSandboxed(false)
	if got, _ := c.Decide(call("bash", false, `{"command":"npm test"}`)); got != Ask {
		t.Error("without a sandbox, commands ask as before")
	}
}

func TestWebRules(t *testing.T) {
	c := NewChecker(ModeDefault, Rules{Allow: []string{"web_fetch(domain:go.dev)", "web_search"}, Deny: []string{"web_fetch(domain:evil.example)"}}, "/w")
	cases := map[string]Decision{
		`{"url":"https://go.dev/doc"}`:           Allow,
		`{"url":"https://pkg.go.dev/net/http"}`:  Allow, // subdomain
		`{"url":"https://notgo.dev/"}`:           Ask,
		`{"url":"https://x.evil.example/steal"}`: Deny,
	}
	for in, want := range cases {
		if got, _ := c.Decide(call("web_fetch", false, in)); got != want {
			t.Errorf("%s: got %v want %v", in, got, want)
		}
	}
	if got, _ := c.Decide(call("web_search", false, `{"query":"x"}`)); got != Allow {
		t.Error("web_search allow rule")
	}
	if r := SuggestRule("web_fetch", json.RawMessage(`{"url":"https://Docs.Python.org/3/"}`)); r != "web_fetch(domain:docs.python.org)" {
		t.Errorf("suggest = %s", r)
	}
	plan := NewChecker(ModePlan, Rules{}, "/w")
	if got, _ := plan.Decide(call("web_fetch", false, `{"url":"https://a.b"}`)); got != Ask {
		t.Error("plan mode should ask (not deny) for web tools")
	}
}

package desktop

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"larik/internal/status"
)

func TestProgramStatusRecords(t *testing.T) {
	var records statusRecords
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	if got, changed := records.apply("state=working:app=deploy:msg=" + encode("Deploying")); !changed || got.State != status.Working || got.Msg != "Deploying" {
		t.Fatalf("root working: %+v, %v", got, changed)
	}
	if got, changed := records.apply("state=blocked:id=build/test:kind=permission:title=" + encode("Tests") + ":msg=" + encode("Approve deploy?")); !changed || got.State != status.Blocked || got.Title != "Tests" || got.Kind != status.Permission {
		t.Fatalf("blocked child: %+v, %v", got, changed)
	}
	if got, changed := records.apply("state=clear:id=build"); !changed || got.State != status.Working {
		t.Fatalf("clear subtree: %+v, %v", got, changed)
	}
	if _, changed := records.apply("state=blocked:msg=" + encode("bad\nline")); changed {
		t.Fatal("accepted control character in message")
	}
	if _, changed := records.apply("state=done:title=A"); changed {
		t.Fatal("accepted bad base64")
	}
	if _, changed := records.apply("state=working:id=bad//id"); changed {
		t.Fatal("accepted bad id")
	}
	if _, changed := records.apply("state=blocked:id=bad!:msg=" + encode("fake")); changed {
		t.Fatal("invalid id overwrote root")
	}
	if got, changed := records.apply("state=done:msg=" + encode("Deployed")); !changed || got.State != status.Done {
		t.Fatalf("done: %+v, %v", got, changed)
	}
	if got, changed := records.dropLive(); changed || got.State != status.Done {
		t.Fatalf("done should survive exit: %+v, %v", got, changed)
	}
	if got, changed := records.apply("state=clear"); !changed || got.State != status.Idle {
		t.Fatalf("clear root: %+v, %v", got, changed)
	}
}

func TestProgramStatusLimits(t *testing.T) {
	if _, _, _, ok := parseProgramStatus("state=working:msg=" + strings.Repeat("A", 2733)); ok {
		t.Fatal("accepted oversized message")
	}
	var records statusRecords
	for i := range 300 {
		id := "job" + strings.Repeat("x", i%3) + string(rune('A'+i/26)) + string(rune('A'+i%26))
		records.apply("state=working:id=" + id)
	}
	if len(records.byID) > 256 || len(records.order) > 256 {
		t.Fatalf("record cap: %d records, %d order", len(records.byID), len(records.order))
	}
}

func TestTerminalTabShowsProgramStatus(t *testing.T) {
	a := New(Options{})
	tb := &tab{id: "terminal", dir: "/project", cmd: []string{"sh"}}
	other := &tab{id: "other", dir: "/project"}
	a.tabs = []*tab{tb, other}
	a.activate(other)
	report, _ := tb.records.apply("state=blocked:kind=auth:title=" + base64.StdEncoding.EncodeToString([]byte("Deploy")) + ":msg=" + base64.StdEncoding.EncodeToString([]byte("Sign in")))
	a.setProgramStatus(tb, report)
	if got := a.tabStatus(tb); got.State != status.Blocked || got.Kind != status.Auth {
		t.Fatalf("tab status: %+v", got)
	}
	if a.tabTitle(tb) != "Deploy" || !strings.Contains(a.where(tb), "Sign in") {
		t.Fatalf("tab title %q, location %q", a.tabTitle(tb), a.where(tb))
	}
	if !a.needsLook(tb) || len(a.events) != 1 || a.events[0].read {
		t.Fatalf("blocked status did not draw attention: %v, %+v", a.needsLook(tb), a.events)
	}
	bar := ui.NewTester(func(c *ui.Context) { a.tabBar(c, c.Theme()) }, 900, 44)
	bar.Frame()
	snapshot(t, bar, "terminal-program-status.png")
	a.activate(tb)
	if a.needsLook(tb) || !a.events[0].read {
		t.Fatal("looking at the terminal did not clear its alert")
	}
}

func TestStatusCommandUpdatesOnlyTerminalTabs(t *testing.T) {
	a := New(Options{})
	defer a.Close()
	tb := &tab{id: "terminal", dir: "/project", cmd: []string{"sh"}}
	other := &tab{id: "session", dir: "/project"}
	a.tabs = []*tab{tb, other}
	a.activate(other)
	res := a.control(Request{Cmd: "status", Tab: tb.id, StatusID: "claude", State: "blocked", Kind: "permission", Title: "Claude Code", Message: "Needs permission"})
	if res.Error != "" || a.tabStatus(tb).State != status.Blocked || a.tabTitle(tb) != "Claude Code" || len(a.events) != 1 {
		t.Fatalf("status did not update the terminal: %+v %+v", res, a.tabStatus(tb))
	}
	res = a.control(Request{Cmd: "status", Tab: tb.id, StatusID: "claude", State: "done"})
	if res.Error != "" || a.tabStatus(tb).State != status.Done {
		t.Fatalf("done: %+v %+v", res, a.tabStatus(tb))
	}
	res = a.control(Request{Cmd: "status", Tab: tb.id, StatusID: "claude", State: "clear"})
	if res.Error != "" || a.tabStatus(tb).State != status.Idle {
		t.Fatalf("clear: %+v %+v", res, a.tabStatus(tb))
	}
	for _, ref := range []string{"missing", other.id} {
		if res := a.control(Request{Cmd: "status", Tab: ref, StatusID: "claude", State: "working"}); res.Error == "" {
			t.Fatalf("status accepted %s", ref)
		}
	}
	if _, err := statusPayload(Request{StatusID: "bad/id/!", State: "working"}); err == nil {
		t.Fatal("accepted invalid record ID")
	}
	if _, err := statusPayload(Request{StatusID: "claude", State: "blocked", Kind: "wrong"}); err == nil {
		t.Fatal("accepted invalid blocked kind")
	}
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"larik/internal/bench"
	"larik/internal/config"
	"larik/internal/subagent"
	"larik/internal/tools"
)

func TestBenchModelSpec(t *testing.T) {
	if got := benchModelSpec("main"); got != "" {
		t.Errorf("main resolved as %q, want the empty primary-model spec", got)
	}
	if got := benchModelSpec("worker"); got != "worker" {
		t.Errorf("worker resolved as %q", got)
	}
}

func TestCompactionChange(t *testing.T) {
	if got := compactionChange(16_000); got != "~16.0k context freed" {
		t.Errorf("positive change = %q", got)
	}
	if got := compactionChange(-500); got != "~500 context added" {
		t.Errorf("negative change = %q", got)
	}
}

func TestSpread(t *testing.T) {
	n := func(v int) string { return fmt.Sprint(v) }
	for _, c := range []struct {
		xs   []int
		want string
	}{
		{[]int{5}, "5"},
		{[]int{4, 4, 4}, "4"},
		{[]int{9, 1, 5}, "5 (1–9)"},
		{[]int{2, 8, 4, 6}, "5 (2–8)"},
	} {
		if got := spread(c.xs, n); got != c.want {
			t.Errorf("spread(%v) = %q, want %q", c.xs, got, c.want)
		}
	}
	if xs := []int{3, 1, 2}; spread(xs, n) != "2 (1–3)" || xs[0] != 3 {
		t.Error("spread must not reorder its input")
	}
}

func TestBenchHarnesses(t *testing.T) {
	if hs, err := benchHarnesses("", "", ""); err != nil || len(hs) != 1 || hs[0].dir != "" {
		t.Fatalf("no flag = %v, %v; want only the default harness", hs, err)
	}
	base := t.TempDir()
	a, b := filepath.Join(base, "terse"), filepath.Join(base, "verbose")
	for _, d := range []string{a, b, filepath.Join(base, "x", "terse")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hs, err := benchHarnesses("none,"+a+", "+b, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 3 || hs[0].dir != "" || hs[1].name != "terse" || hs[2].dir != b {
		t.Errorf("harnesses = %+v", hs)
	}
	if _, err := benchHarnesses(filepath.Join(base, "missing"), "", ""); err == nil {
		t.Error("a missing directory should be rejected")
	}
	if _, err := benchHarnesses(a+","+filepath.Join(base, "x", "terse"), "", ""); err == nil {
		t.Error("two harnesses with the same name would share result rows")
	}
}

func TestBenchRole(t *testing.T) {
	cfg := &config.Config{}
	dir := t.TempDir()
	if _, err := benchRole("", "minimal", cfg, dir, dir); err == nil {
		t.Error("--context without --as should be rejected")
	}
	if r, err := benchRole("", "", cfg, dir, dir); err != nil || r.def != nil || len(r.contexts) != 1 {
		t.Errorf("no --as = %+v, %v; want one main-agent run", r, err)
	}
	r, err := benchRole("worker", "", cfg, dir, dir)
	if err != nil || r.def.Name != "general-purpose" || r.maxTurns != subagent.DefaultMaxTurns || !r.canWrite || len(r.contexts) != 1 || r.contexts[0] {
		t.Errorf("worker = %+v, %v", r, err)
	}
	r, err = benchRole("explore", "full, minimal", cfg, dir, dir)
	if err != nil || r.canWrite || !slices.Equal(r.contexts, []bool{false, true}) {
		t.Fatalf("explore = %+v, %v", r, err)
	}
	var names []string
	for _, task := range r.tasks(bench.Tasks()) {
		names = append(names, task.Name)
	}
	if !slices.Equal(names, []string{"find-undocumented", "log-summary"}) {
		t.Errorf("explore's tasks = %v, want only those with a reply form", names)
	}
	if _, err := benchRole("explore", "tiny", cfg, dir, dir); err == nil {
		t.Error("an unknown context setting should be rejected")
	}
	if _, err := benchRole("nobody", "", cfg, dir, dir); err == nil {
		t.Error("an unknown agent should be rejected")
	}
}

func TestBenchSettingsAndWidth(t *testing.T) {
	plain := benchSettings("m", []tools.Execution{tools.DefaultExecution}, []benchHarness{{name: "none"}}, benchRunRole{contexts: []bool{false}})
	if len(plain) != 1 || plain[0].row != "m" {
		t.Errorf("a plain run should keep the model's label: %+v", plain)
	}
	explore, _ := subagent.Discover(nil).Get("explore")
	role := benchRunRole{name: "explore", def: &explore, contexts: []bool{false, true}}
	var rows []string
	for _, st := range benchSettings("a-rather-long/provider-model-name", []tools.Execution{tools.DefaultExecution}, []benchHarness{{name: "none"}, {name: "personal", dir: "/x"}}, role) {
		rows = append(rows, st.row)
	}
	want := []string{
		"a-rather-long/provider-model-name {none} as explore <full>",
		"a-rather-long/provider-model-name {none} as explore <minimal>",
		"a-rather-long/provider-model-name {personal} as explore <full>",
		"a-rather-long/provider-model-name {personal, unseen} as explore <minimal>",
	}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %q", rows)
	}
	if got := labelWidth(rows); got != len(want[3]) {
		t.Errorf("width = %d, want the longest label's %d", got, len(want[3]))
	}
	if got := labelWidth([]string{"short"}); got != 40 {
		t.Errorf("width of short labels = %d, want 40", got)
	}
}

// With --as and minimal context the harness never reaches the prompt, so
// those rows are marked and the run warns, once.
func TestHarnessWarning(t *testing.T) {
	explore, _ := subagent.Discover(nil).Get("explore")
	variants := []benchHarness{{name: "personal", dir: "/x"}, {name: "h-finish", dir: "/y"}}
	for name, c := range map[string]struct {
		harnesses []benchHarness
		role      benchRunRole
		warn      bool
	}{
		"minimal subagent": {variants, benchRunRole{name: "explore", def: &explore, contexts: []bool{false, true}}, true},
		"full subagent":    {variants, benchRunRole{name: "explore", def: &explore, contexts: []bool{false}}, false},
		"main agent":       {variants, benchRunRole{contexts: []bool{false}}, false},
		"no harness dir":   {[]benchHarness{{name: "none"}}, benchRunRole{name: "explore", def: &explore, contexts: []bool{true}}, false},
	} {
		if got := harnessWarning(c.harnesses, c.role) != ""; got != c.warn {
			t.Errorf("%s: warning = %v, want %v", name, got, c.warn)
		}
	}
}

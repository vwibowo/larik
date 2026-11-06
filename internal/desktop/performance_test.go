package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

// benchmarkSidebar supplies enough sessions to make per-frame searches
// visible without reading the real session store or starting programs.
func benchmarkSidebar(count int, query string) *App {
	a := New(Options{Version: "dev"})
	a.query, a.convAsked, a.convQuery = query, query, query
	a.projects = make([]session.Project, 20)
	for i := range a.projects {
		root := fmt.Sprintf("/benchmark/project-%02d", i)
		a.projects[i] = session.Project{Root: root, Name: fmt.Sprintf("project-%02d", i)}
		open := true
		a.open[root] = &open
	}
	for i := range count {
		p := &a.projects[i%len(a.projects)]
		title := fmt.Sprintf("Session %04d", i)
		if i%10 == 0 {
			title += " needle"
		}
		p.Sessions = append(p.Sessions, session.Indexed{Info: session.Info{
			ID: fmt.Sprintf("session-%04d", i), Path: fmt.Sprintf("%s/%04d.jsonl", p.Root, i),
			Title: title, Cwd: p.Root, Modified: time.Unix(int64(i), 0),
		}})
	}
	return a
}

// benchmarkChangedRepo supplies a Changes pane's idle repository.
func benchmarkChangedRepo(b *testing.B) (string, headCache) {
	dir := b.TempDir()
	run := func(args ...string) {
		b.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "benchmark@example.test")
	run("config", "user.name", "Benchmark")
	for i := range 100 {
		path := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-qm", "initial")
	for i := range 100 {
		path := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	heads := headCache{}
	if got := readChanges(dir, heads, compareCommit); got.err != nil || len(got.snaps) != maxChangedFiles {
		b.Fatalf("warm read: %v, %d files", got.err, len(got.snaps))
	}
	return dir, heads
}

// BenchmarkChangesIdle measures a full Changes refresh with tracked
// files whose contents have not changed since the last read.
func BenchmarkChangesIdle(b *testing.B) {
	dir, heads := benchmarkChangedRepo(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if got := readChanges(dir, heads, compareCommit); got.err != nil {
			b.Fatal(got.err)
		}
	}
}

func BenchmarkChangesProbeIdle(b *testing.B) {
	dir, _ := benchmarkChangedRepo(b)
	root, indexPath := changesRepo(dir)
	if root == "" || indexPath == "" {
		b.Fatal("cannot resolve benchmark repository")
	}
	want, err := changesFingerprint(context.Background(), root, indexPath)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := changesFingerprint(context.Background(), root, indexPath)
		if err != nil || got != want {
			b.Fatalf("idle fingerprint changed: %q, %v", got, err)
		}
	}
}

func BenchmarkSidebarFrame(b *testing.B) {
	for _, count := range []int{500, 2000} {
		for _, query := range []string{"", "needle"} {
			name := fmt.Sprintf("sessions=%d/search=%t", count, query != "")
			b.Run(name, func(b *testing.B) {
				a := benchmarkSidebar(count, query)
				tester := ui.NewTester(func(c *ui.Context) {
					a.sidebar(c, a.palette(c.Theme()), false)
				}, 280, 800)
				tester.Frame()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					tester.Frame()
				}
			})
		}
	}
}

func BenchmarkSearchMatches(b *testing.B) {
	for _, count := range []int{500, 2000} {
		for _, query := range []string{"", "needle"} {
			name := fmt.Sprintf("sessions=%d/search=%t", count, query != "")
			b.Run(name, func(b *testing.B) {
				a := benchmarkSidebar(count, query)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					_ = a.matches()
				}
			})
		}
	}
}

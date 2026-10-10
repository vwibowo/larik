package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/app"
	"larik/internal/config"
)

func TestStartupModelShowsLoadingThenHandsOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startup := &startupModel{ctx: ctx, cancel: cancel}
	view := startup.View()
	if !view.AltScreen || !strings.Contains(view.Content, "Starting Larik") {
		t.Fatalf("loading view = %#v", view)
	}
	startup.Update(tea.WindowSizeMsg{Width: 132, Height: 47})
	ready := testModel(t)
	next, _ := startup.Update(startupResult{opts: ready.opts})
	if next != startup.live || startup.live == nil || startup.live.agent == nil {
		t.Fatal("startup did not hand off to the ready TUI model")
	}
	if startup.live.width != 132 || startup.live.height != 47 {
		t.Fatalf("ready model size = %dx%d, want 132x47", startup.live.width, startup.live.height)
	}
}

func TestNewModelUsesProjectRootFromApp(t *testing.T) {
	base := testModel(t)
	base.opts.App = &app.App{ProjectRoot: filepath.Join("/repo", "project")}
	m := newModel(base.opts)
	if m.projectRoot != base.opts.App.ProjectRoot {
		t.Fatalf("project root = %q, want %q", m.projectRoot, base.opts.App.ProjectRoot)
	}
}

var startupBenchmarkFrame string

func BenchmarkStartupLoadingFrame(b *testing.B) {
	startup := &startupModel{}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		startupBenchmarkFrame = startup.View().Content
	}
}

func BenchmarkStartupReadyFrame(b *testing.B) {
	for _, sandboxEnabled := range []bool{true, false} {
		name := "sandbox-enabled"
		if !sandboxEnabled {
			name = "sandbox-disabled"
		}
		b.Run(name, func(b *testing.B) {
			cwd := benchmarkStartupProject(b, sandboxEnabled)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				cfg, err := config.Load(cwd) // run() loads this before app.Setup loads it again.
				if err != nil {
					b.Fatal(err)
				}
				app.LoadCatwalkCache(cfg.DataDir)
				a, err := app.Setup(cwd, "benchmark")
				if err != nil {
					b.Fatal(err)
				}
				s, err := a.Open(app.Options{Unattended: true})
				if err != nil {
					a.Close()
					b.Fatal(err)
				}
				m := newModel(Options{App: a, Agent: s.Agent, Session: s, Config: a.Cfg, Hooks: s.Hooks, History: s.History})
				startupBenchmarkFrame = m.View().Content
				b.StopTimer()
				s.Close("other")
				a.Close()
				b.StartTimer()
			}
		})
	}
}

func BenchmarkStartupPhases(b *testing.B) {
	cwd := benchmarkStartupProject(b, true)
	settings := func(b *testing.B) {
		b.Helper()
		if _, err := config.Load(cwd); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("config-load", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := config.Load(cwd); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("setup", func(b *testing.B) {
		cwd := benchmarkStartupProject(b, true)
		b.ReportAllocs()
		for range b.N {
			a, err := app.Setup(cwd, "benchmark")
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			a.Close()
			b.StartTimer()
		}
	})
	b.Run("setup-sandbox-disabled", func(b *testing.B) {
		cwd := benchmarkStartupProject(b, false)
		b.ReportAllocs()
		for range b.N {
			a, err := app.Setup(cwd, "benchmark")
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			a.Close()
			b.StartTimer()
		}
	})

	b.Run("open-session", func(b *testing.B) {
		settings(b)
		a, err := app.Setup(cwd, "benchmark")
		if err != nil {
			b.Fatal(err)
		}
		defer a.Close()
		b.ReportAllocs()
		for range b.N {
			s, err := a.Open(app.Options{Unattended: true})
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			s.Close("other")
			b.StartTimer()
		}
	})

	b.Run("model-first-view", func(b *testing.B) {
		settings(b)
		a, err := app.Setup(cwd, "benchmark")
		if err != nil {
			b.Fatal(err)
		}
		defer a.Close()
		s, err := a.Open(app.Options{Unattended: true})
		if err != nil {
			b.Fatal(err)
		}
		defer s.Close("other")
		opts := Options{App: a, Agent: s.Agent, Session: s, Config: a.Cfg, Hooks: s.Hooks, History: s.History}
		b.ReportAllocs()
		for range b.N {
			m := newModel(opts)
			startupBenchmarkFrame = m.View().Content
		}
	})
}

func benchmarkStartupProject(b *testing.B, sandboxEnabled bool) string {
	b.Helper()
	root := b.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	cwd := filepath.Join(root, "project")
	configDir := filepath.Join(configHome, "larik")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		b.Fatal(err)
	}
	settings := `{"model":"ollama/test","providers":{"ollama":{"base_url":"http://127.0.0.1:1"}}}`
	if !sandboxEnabled {
		settings = `{"model":"ollama/test","providers":{"ollama":{"base_url":"http://127.0.0.1:1"}},"sandbox":{"enabled":false}}`
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(settings), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("XDG_CONFIG_HOME", configHome)
	b.Setenv("XDG_DATA_HOME", dataHome)
	b.Setenv("HOME", filepath.Join(root, "home"))
	return cwd
}

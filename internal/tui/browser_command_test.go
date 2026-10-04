package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/app"
)

func TestBrowserCommandRequiresManagedSession(t *testing.T) {
	m := testModel(t)
	m.command("/browser")
	if m.opts.Config.Browser.Enabled {
		t.Fatal("browser should default to off")
	}
	m.command("/browser on")
	if _, err := os.Stat(m.opts.Config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unmanaged sessions must not persist a toggle: %v", err)
	}
}

func TestBrowserToggleReloadsServices(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "larik", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"model":"local/test","providers":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:1/v1"}},"browser":{"chrome_path":"/nonexistent/larik-chrome"}}`
	if err := os.WriteFile(configPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	s, err := a.Open(app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Options{App: a, Config: a.Cfg, Agent: s.Agent, Session: s})
	m.command("/browser on")
	if !m.opts.Config.Browser.Enabled || m.opts.App == a || m.opts.App.BrowserNote == "" || m.agent.HasContext() {
		t.Fatal("enabling must persist and reload into a fresh context; unavailable Chrome must not advertise tools")
	}
	if !strings.Contains(savedConfig(t, m), `"enabled": true`) {
		t.Fatal("browser switch was not saved")
	}
	m.command("/browser off")
	if m.opts.Config.Browser.Enabled || m.opts.App.BrowserNote != "" || !strings.Contains(savedConfig(t, m), `"enabled": false`) {
		t.Fatal("disabling must persist and rebuild services")
	}
	t.Cleanup(func() { m.sess.Close("other"); m.opts.App.Close() })
}

func TestBrowserToggleCancelDoesNotSave(t *testing.T) {
	m := savedMessagesModel(t)
	m.opts.App = &app.App{} // confirmation must not invoke setup until approved
	m.sess = &app.Session{}
	m.command("/browser on")
	if m.reload == nil {
		t.Fatal("changing tools with a model context needs confirmation")
	}
	m.Update(press(tea.KeyEscape))
	if m.reload != nil {
		t.Fatal("cancel should dismiss confirmation")
	}
	if _, err := os.Stat(m.opts.Config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("cancel should not save the browser setting: %v", err)
	}
	m.command("/browser nope")
	if m.reload != nil {
		t.Fatal("invalid arguments must not request a reload")
	}
}

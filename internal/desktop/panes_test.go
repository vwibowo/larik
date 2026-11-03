package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func TestSplitFollowsFocus(t *testing.T) {
	a := New(Options{Cwd: "/start"})
	one, two, three := &tab{id: "1", dir: "/one"}, &tab{id: "2", dir: "/two"}, &tab{id: "3", dir: "/three"}
	a.tabs = []*tab{one, two, three}
	a.activate(one)
	a.showInSplit(two, false)
	if a.focused() != two || a.currentDir() != "/two" {
		t.Fatalf("the split has the keyboard: %v", a.focused().id)
	}
	// A tab chosen while the split is focused opens there.
	a.activate(three)
	if shownAt(a, 0) != one || shownAt(a, 1) != three {
		t.Fatalf("chosen into the split: %s | %s", shownAt(a, 0).id, shownAt(a, 1).id)
	}
	// Choosing the first pane's tab moves the keyboard back.
	a.activate(one)
	if a.focus != a.panes()[0] || a.focused() != one {
		t.Fatal("the first pane's tab takes the keyboard")
	}
	// Closing the first pane's tab hands its room to the split.
	a.closeTab(one)
	if shownAt(a, 0) != three || a.split() || a.focused() != three {
		t.Fatalf("after closing the first tab: %v %v", shownAt(a, 0), shownAt(a, 1))
	}
	a.showInSplit(two, false)
	a.closeTab(two)
	if a.split() || shownAt(a, 0) != three || len(a.tabs) != 1 {
		t.Fatal("closing the split's tab unsplits")
	}
}

func TestAttendAlertsWhatChanged(t *testing.T) {
	a := New(Options{})
	var alerts []string
	badge := -1
	a.alert = func(s session.Indexed) { alerts = append(alerts, s.Title+":"+string(s.Status.State)) }
	a.badge = func(n int) { badge = n }
	index := func(states ...status.State) []session.Project {
		p := session.Project{Root: "/r"}
		for i, st := range states {
			s := session.Indexed{Status: status.Report{State: st}}
			s.Path, s.Title = string(rune('a'+i)), string(rune('a'+i))
			p.Sessions = append(p.Sessions, s)
		}
		return []session.Project{p}
	}
	a.setProjects(index(status.Working, status.Blocked, status.Idle))
	if len(alerts) != 0 || badge != 1 {
		t.Fatalf("the first index raises nothing, and counts what waits: %q %d", alerts, badge)
	}
	a.setProjects(index(status.Done, status.Blocked, status.Blocked))
	if strings.Join(alerts, " ") != "a:done c:blocked" {
		t.Fatalf("alerts: %q", alerts)
	}
}

func TestNextWaiting(t *testing.T) {
	data, _ := fixture(t)
	a := New(Options{DataDir: data})
	a.setProjects(index(t, data))
	a.nextWaiting()
	if a.notice == "" && len(a.tabs) == 0 {
		// The blocked session is open in this test's process, so it
		// stays there; what matters is that it was the one chosen.
		t.Fatal("nothing happened")
	}
	if !strings.Contains(a.notice, "Add OSC 7501 output") {
		t.Fatalf("the blocked session comes first: %q", a.notice)
	}
}

func TestChangesPane(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755)
		os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644)
	}
	run("init", "-q")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("gone.txt", "bye\n")
	run("add", ".")
	run("commit", "-qm", "first")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	os.Remove(filepath.Join(repo, "gone.txt"))
	write("docs/new file.md", "# New\n")
	write("logo.bin", "\x00\x01")

	heads := headCache{}
	r := readChanges(filepath.Join(repo, "docs"), heads, compareCommit)
	root, reads, more, err := r.root, r.snaps, r.more, r.err
	if err != nil || more != 0 {
		t.Fatal(err, more)
	}
	if want, _ := filepath.EvalSymlinks(repo); root != want && root != repo {
		t.Errorf("root %q, want %q", root, repo)
	}
	got := map[string]fileRead{}
	for _, r := range reads {
		got[r.path] = r
	}
	if m := got["main.go"]; strings.TrimSpace(m.code) != "M" || m.adds != 3 || m.dels != 1 || !strings.Contains(m.old, "func main() {}") || !strings.Contains(m.new, "println") {
		t.Errorf("main.go: %+v", m)
	}
	if g := got["gone.txt"]; strings.TrimSpace(g.code) != "D" || g.old != "bye\n" || g.new != "" {
		t.Errorf("gone.txt: %+v", g)
	}
	if n := got["docs/new file.md"]; n.code != "??" || n.new != "# New\n" || n.adds != 1 {
		t.Errorf("new file: %+v", n)
	}
	if b := got["logo.bin"]; b.note != "binary" {
		t.Errorf("binary: %+v", b)
	}

	a := New(Options{})
	ch := &changes{dir: repo}
	ch.apply(r)
	first := ch.files[0].diff
	ch.apply(r)
	if ch.files[0].diff != first {
		t.Error("a file keeps its viewer across reads")
	}
	tt := ui.NewTester(func(c *ui.Context) { a.changesPane(c, a.palette(ui.DarkTheme()), ch) }, 640, 800)
	tt.SetDark(true)
	tt.Frame()
	for _, want := range []string{"Changes", "4 files", "main.go", "+3", "−1", "docs/new file.md", "Not shown: binary"} {
		if !tt.HasText(want) {
			t.Errorf("pane lacks %q: %q", want, tt.Texts())
		}
	}
	snapshot(t, tt, "changes-dark.png")

	// A file's menu opens it, shows it, or copies its path.
	var copied, opened string
	a.copyText = func(s string) { copied = s }
	a.openPath = func(p string) { opened = p }
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Path"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Open"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if want := filepath.Join(root, "main.go"); copied != want || opened != want {
		t.Errorf("copied %q, opened %q, want %q", copied, opened, want)
	}

	// HEAD's text comes from the cache on the next read, while HEAD
	// stays; a new commit reads it again.
	for key := range heads {
		heads[key] = "from the cache\n"
	}
	again := readChanges(repo, heads, compareCommit).snaps
	for _, r := range again {
		if r.path == "main.go" && r.old != "from the cache\n" {
			t.Errorf("main.go read HEAD again: %q", r.old)
		}
	}
	run("commit", "-qam", "next")
	again = readChanges(repo, heads, compareCommit).snaps
	for _, r := range again {
		if r.path == "main.go" {
			t.Errorf("main.go still changed after committing it: %+v", r)
		}
	}
	if len(heads) > len(again) {
		t.Errorf("the cache kept %d texts for %d changes", len(heads), len(again))
	}

	err = readChanges(t.TempDir(), headCache{}, compareCommit).err
	if err == nil || !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("outside a repository: %v", err)
	}
}

func TestInstallLauncher(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no launcher here")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	bin := "/opt/it's here/larik"
	where, err := Install(bin)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(where, "Contents", "MacOS", "larik-desktop")
	quoted := `'/opt/it'\''s here/larik'`
	if runtime.GOOS == "darwin" {
		// larik runs through a link in the bundle; the app has its icon.
		if dest, err := os.Readlink(filepath.Join(where, "Contents", "MacOS", "larik")); err != nil || dest != bin {
			t.Errorf("link: %q %v", dest, err)
		}
		if icon, err := os.ReadFile(filepath.Join(where, "Contents", "Resources", "larik.icns")); err != nil || string(icon[:4]) != "icns" {
			t.Errorf("icon: %v", err)
		}
		quoted = `"$(cd "$(dirname "$0")" && pwd)/larik"`
	}
	if runtime.GOOS == "linux" {
		script = filepath.Join(home, ".local", "share", "larik", "larik-desktop")
		entry, _ := os.ReadFile(where)
		if !strings.Contains(string(entry), "Exec=\""+script+"\"") {
			t.Errorf("entry: %s", entry)
		}
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), quoted) {
		t.Errorf("the script runs bin, quoted: %s", data)
	}
	// The script is valid shell.
	if out, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v %s", err, out)
	}
	removed, err := Uninstall()
	if err != nil || len(removed) == 0 {
		t.Fatal(removed, err)
	}
	if _, err := os.Stat(where); !os.IsNotExist(err) {
		t.Errorf("still there: %v", err)
	}
}

func TestSplitAndChangesInTheWindow(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	os.RemoveAll(filepath.Join(repo, ".git"))
	if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
		t.Skip("no git")
	}
	os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Notes\n"), 0o644)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()

	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	main := shownAt(a, 0)
	a.splitRight()
	right := shownAt(a, 1)
	if right == nil || a.focused() != right || right.dir != repo || len(a.tabs) != 2 {
		mu.Unlock()
		t.Fatalf("split: %+v", right)
	}
	tt := ui.NewTester(a.view, 1400, 800)
	tt.SetDark(true)
	mu.Unlock()
	frameUntil(t, mu, tt, "both panes' larik", func() bool {
		return strings.Contains(main.term.Text(), "args:") && strings.Contains(right.term.Text(), "args: tab:"+right.id)
	})
	mu.Lock()
	tt.Frame()
	if a.focused() != right {
		t.Error("the new split keeps the keyboard")
	}
	snapshot(t, tt, "split-dark.png")
	if err := tt.Click("Close pane (the tab stays open)"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	if a.split() || a.shown(main) || !a.shown(right) || len(a.tabs) != 2 {
		t.Errorf("closing the first pane keeps its tab, and gives the split its room")
	}
	if err := tt.Click("Changes in this project (" + keyLabel(true, "G") + ")"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	ch := a.changes
	mu.Unlock()
	frameUntil(t, mu, tt, "changes", func() bool { return ch.loaded })
	mu.Lock()
	defer mu.Unlock()
	tt.Frame()
	if !tt.HasText("notes.md") {
		t.Errorf("changes pane: %q", tt.Texts())
	}
	snapshot(t, tt, "changes-window-dark.png")
	// The changes beside a split: both stay.
	a.showInSplit(main, false)
	tt.Frame()
	if shownAt(a, 1) != main || a.changes != ch || !tt.HasText("notes.md") {
		t.Errorf("a split beside the changes: right %v, changes %v", shownAt(a, 1) != nil, a.changes != nil)
	}
	snapshot(t, tt, "split-changes-dark.png")
}

func TestTabsComeBackNextLaunch(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	bin := fakeLarik(t)
	first, mu := serialApp(Options{Bin: bin, DataDir: data, Cwd: repo})
	mu.Lock()
	first.setProjects(index(t, data))
	if first.restore() {
		t.Fatal("nothing to restore on the first launch")
	}
	first.selectStart()
	main := shownAt(first, 0).path
	var site string
	for _, p := range first.projects {
		if p.Name == "ornith-page" {
			site = p.Sessions[0].Path
		}
	}
	_, mainProject, _ := first.find(main)
	*first.open[mainProject.Root] = false
	first.noSidebar = true
	first.splitRight()        // focused, so the session chosen next opens there
	first.selectSession(site) // opens in the focused split
	if shownAt(first, 1) == nil || shownAt(first, 1).path != site {
		mu.Unlock()
		t.Fatalf("split: %+v", shownAt(first, 1))
	}
	first.saveState()
	mu.Unlock()
	waitFor(t, mu, "state saved", func() bool {
		data, _ := os.ReadFile(first.statePath())
		return strings.Contains(string(data), filepath.Base(site))
	})
	first.Close()
	// Closing ends every tab; that isn't the user closing them.
	time.Sleep(100 * time.Millisecond)
	if saved, _ := os.ReadFile(first.statePath()); !strings.Contains(string(saved), filepath.Base(main)) {
		t.Fatalf("closing the window kept the tabs: %s", saved)
	}

	next, mu2 := serialApp(Options{Bin: bin, DataDir: data, Cwd: repo})
	defer next.Close()
	mu2.Lock()
	defer mu2.Unlock()
	next.setProjects(index(t, data))
	if !next.restore() || len(next.tabs) != 2 || shownAt(next, 0).path != main || shownAt(next, 1) == nil || shownAt(next, 1).path != site {
		t.Fatalf("restored: %d tabs, active %v, right %v", len(next.tabs), shownAt(next, 0), shownAt(next, 1))
	}
	if !next.noSidebar || *next.open[mainProject.Root] {
		t.Fatalf("the sidebar: hidden %v, %s open %v", next.noSidebar, mainProject.Name, *next.open[mainProject.Root])
	}

	// The sidebar comes back with its key, and each project can start a
	// session of its own.
	tt := ui.NewTester(next.view, 1400, 800)
	tt.Frame()
	if tt.HasText("Inbox") {
		t.Fatal("the hidden sidebar shows")
	}
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	tt.Key(mod, ui.KeyB)
	tt.Frame()
	if !tt.HasText("Inbox") {
		t.Fatalf("%s didn't show the sidebar", keyLabel(false, "B"))
	}
	if err := tt.Click("New session in ornith-page"); err != nil {
		t.Fatal(err)
	}
	if len(next.tabs) != 3 || filepath.Base(next.focused().dir) != "ornith-page" {
		t.Fatalf("new session in the project: %d tabs, %q", len(next.tabs), next.focused().dir)
	}
}

func TestSearchAndSessionMenu(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	// The search for what was said reports back through the window's
	// updates, which wait for the test.
	mu.Lock()
	defer mu.Unlock()
	var copied string
	a.copyText = func(s string) { copied = s }
	a.setProjects(index(t, data))

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("Search sessions (" + keyLabel(false, "K") + ")"); err != nil {
		t.Fatal(err)
	}
	tt.Type("landing ornith")
	tt.Frame()
	snapshot(t, tt, "search-dark.png")
	if !tt.HasText("1 found") || !tt.HasText("Polish the landing copy") || tt.HasText("Refactor the parser and run the tests") {
		t.Fatalf("search: %q", tt.Texts())
	}
	if err := tt.RightClick("Polish the landing copy"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Session ID"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if found := a.matches(); len(found) != 1 || copied != found[0].ID {
		t.Fatalf("copied %q", copied)
	}
	a.query = "nothing like this"
	tt.Frame()
	if !tt.HasText("Nothing found") {
		t.Fatalf("no match: %q", tt.Texts())
	}
}

func TestOpenInSplitAndFork(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	main := shownAt(a, 0)
	var site session.Indexed
	for _, p := range a.projects {
		if p.Name == "ornith-page" {
			site = p.Sessions[0]
		}
	}
	a.openInSplit(site.Path, false)
	if shownAt(a, 0) != main || shownAt(a, 1) == nil || shownAt(a, 1).path != site.Path || a.focused() != shownAt(a, 1) {
		mu.Unlock()
		t.Fatalf("open in split: %v %v", shownAt(a, 0), shownAt(a, 1))
	}
	a.fork(site.Path)
	forked := a.focused()
	if len(a.tabs) != 3 || forked.path != "" {
		mu.Unlock()
		t.Fatalf("fork: %d tabs, %q", len(a.tabs), forked.path)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "forked larik", func() bool {
		return strings.Contains(forked.term.Text(), "args:--resume "+site.ID+" --fork tab:")
	})
}

func TestSearchKeys(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	field := "Search sessions (" + keyLabel(false, "K") + ")"
	if err := tt.Click(field); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	tt.Type("landing")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	opened := shownAt(a, 0)
	if opened == nil || a.query != "" || !strings.Contains(a.tabTitle(opened), "landing") {
		mu.Unlock()
		t.Fatalf("enter: %v, query %q", opened, a.query)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "the session's terminal to have the keyboard", func() bool { return !tt.Focused(field) })

	mu.Lock()
	defer mu.Unlock()
	if err := tt.Click(field); err != nil {
		t.Fatal(err)
	}
	tt.Type("parser")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.query != "" || !tt.Focused(field) {
		t.Fatalf("first escape: query %q, field focused %v", a.query, tt.Focused(field))
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if tt.Focused(field) {
		t.Fatal("second escape left the keyboard in the search field")
	}
}

func TestTabMenu(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	var copied string
	a.copyText = func(s string) { copied = s }
	a.setProjects(index(t, data))
	paths := map[string]string{}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			paths[s.Title] = s.Path
		}
	}
	site, parser := paths["Polish the landing copy"], paths["Refactor the parser and run the tests"]
	a.selectSession(site)
	a.selectSession(parser)
	a.newTab(t.TempDir(), "", "")
	siteTab, parserTab := a.tabOf(site), a.tabOf(parser)

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.RightClick("Polish the landing copy tab"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Open in Split"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if shownAt(a, 1) != siteTab || a.focused() != siteTab {
		t.Fatalf("open in split: right %v", shownAt(a, 1))
	}
	if err := tt.RightClick("Polish the landing copy tab"); err != nil {
		t.Fatal(err)
	}
	if menu := strings.Join(tt.Menu(), ","); strings.Contains(menu, "Open in Split") {
		t.Fatalf("a shown tab offers the split: %s", menu)
	}
	if err := tt.ChooseMenuItem("Copy Session ID"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if s, _, _ := a.find(site); copied != s.ID {
		t.Fatalf("copied %q", copied)
	}
	if err := tt.RightClick("Refactor the parser and run the tests tab"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Close Other Tabs"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if len(a.tabs) != 1 || shownAt(a, 0) != parserTab || shownAt(a, 1) != nil {
		t.Fatalf("close others: %d tabs, active %v, right %v", len(a.tabs), shownAt(a, 0), shownAt(a, 1))
	}
}

// The terminal binds Command+K to clearing the screen on macOS; the
// window's search takes it first, and the terminal keeps its text.
func TestSearchKeyFromTheTerminal(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "larik to start", func() bool { return shownAt(a, 0).term.Text() != "" })
	mu.Lock()
	tt.Type("zq") // echoed only if the terminal has the keyboard
	mu.Unlock()
	frameUntil(t, mu, tt, "typing to reach the terminal", func() bool { return strings.Contains(shownAt(a, 0).term.Text(), "zq") })
	mu.Lock()
	defer mu.Unlock()
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	tt.Key(mod, ui.KeyK)
	tt.Frame()
	if !tt.Focused("Search sessions ("+keyLabel(false, "K")+")") || !strings.Contains(shownAt(a, 0).term.Text(), "zq") {
		t.Fatalf("search focused %v, terminal %q", tt.Focused("Search sessions ("+keyLabel(false, "K")+")"), shownAt(a, 0).term.Text())
	}
}

func TestClosingAWorkingTabAsks(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	var site string
	for i, p := range a.projects {
		for j, s := range p.Sessions {
			if s.Title == "Polish the landing copy" {
				site = s.Path
				a.projects[i].Sessions[j].Status = status.Report{State: status.Working}
			}
		}
	}
	a.selectSession(site)
	idle := a.startTab(t.TempDir(), "", "")
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	tt.Key(mod, ui.KeyW)
	tt.Frame()
	if len(a.tabs) != 2 || !tt.HasText("Close “Polish the landing copy”?") {
		t.Fatalf("closing a working tab: %d tabs, %q", len(a.tabs), tt.Texts())
	}
	snapshot(t, tt, "close-working.png")
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 2 || a.confirming {
		t.Fatalf("cancel: %d tabs", len(a.tabs))
	}

	// Closing the others asks too, naming the one working.
	a.activate(idle)
	a.closeOthers(idle)
	tt.Frame()
	if !tt.HasText("Close “Polish the landing copy”?") {
		t.Fatalf("close others: %q", tt.Texts())
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 1 || a.tabs[0] != idle || shownAt(a, 0) != idle {
		t.Fatalf("close: %d tabs", len(a.tabs))
	}

	// An idle tab closes without asking.
	tt.Key(mod, ui.KeyW)
	tt.Frame()
	if len(a.tabs) != 0 || a.confirming {
		t.Fatalf("idle tab: %d tabs, asking %v", len(a.tabs), a.confirming)
	}
}

func TestZoomSizesEveryTab(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	a.restore()
	a.selectStart()
	a.splitRight()
	left, right := shownAt(a, 0), shownAt(a, 1)
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "both terminals sized", func() bool {
		l, _ := left.term.Size()
		r, _ := right.term.Size()
		return l > 0 && r > 0
	})
	mu.Lock()
	defer mu.Unlock()
	cols := func() (int, int) {
		l, _ := left.term.Size()
		r, _ := right.term.Size()
		return l, r
	}
	l0, r0 := cols()
	zoomIn, reset := ui.Ctrl, ui.Ctrl
	if runtime.GOOS == "darwin" {
		zoomIn, reset = ui.Super, ui.Super
	}
	for range 4 {
		tt.Key(zoomIn, ui.KeyEqual) // the split has the keyboard
		tt.Frame()
	}
	tt.Frame()
	if l, r := cols(); a.fontSize != 17 || l >= l0 || r >= r0 {
		t.Fatalf("zoomed to %v: columns %d→%d and %d→%d", a.fontSize, l0, l, r0, r)
	}
	if a.saveState(); !strings.Contains(a.saved, `"font_size":17`) {
		t.Fatalf("not saved: %s", a.saved)
	}
	tt.Key(reset, ui.Key0)
	tt.Frame()
	tt.Frame()
	if l, r := cols(); a.fontSize != 0 || l != l0 || r != r0 {
		t.Fatalf("reset to %v: columns %d and %d", a.fontSize, l, r)
	}
}

func TestSessionAges(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:     "now",
		5 * time.Minute:      "5m",
		3 * time.Hour:        "3h",
		50 * time.Hour:       "2d",
		30 * 24 * time.Hour:  "4w",
		400 * 24 * time.Hour: "1y",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	snapshot(t, tt, "ages.png")
	// The landing copy was written three hours ago; the blocked session
	// shows its pause sign instead.
	if !tt.HasText("3h") || tt.HasText("1h") {
		t.Fatalf("ages: %q", tt.Texts())
	}
}

func TestFindInTheTerminal(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	tb := shownAt(a, 0)
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "larik to start", func() bool { return strings.Contains(tb.term.Text(), "dir:") })
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	mu.Lock()
	tt.Key(mod, ui.KeyF)
	tt.Frame()
	if !tb.finding || !tt.Focused("Find in the terminal") {
		mu.Unlock()
		t.Fatalf("find bar: open %v", tb.finding)
	}
	tt.Type("resume")
	mu.Unlock()
	frameUntil(t, mu, tt, "a match", func() bool { return tb.search.Total == 1 && !tb.search.Searching })
	mu.Lock()
	defer mu.Unlock()
	tt.Key(0, ui.KeyEnter) // to the match
	tt.Frame()
	tt.Frame()
	if tb.search.Selected != 1 || !tt.HasText("1 of 1") {
		t.Fatalf("enter: %+v", tb.search)
	}
	snapshot(t, tt, "find.png")
	tt.Key(0, ui.KeyEscape) // clears
	tt.Frame()
	if !tb.finding || tb.find != "" {
		t.Fatalf("first escape: open %v, %q", tb.finding, tb.find)
	}
	tt.Key(0, ui.KeyEscape) // closes
	tt.Frame()
	tt.Frame()
	if tb.finding || tt.Focused("Find in the terminal") || tb.search.Total != 0 {
		t.Fatalf("second escape: open %v, search %+v", tb.finding, tb.search)
	}
}

func TestDragTabsToReorder(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	paths := map[string]string{}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			paths[s.Title] = s.Path
		}
	}
	a.selectSession(paths["Polish the landing copy"])
	a.selectSession(paths["Refactor the parser and run the tests"])
	a.newTab(t.TempDir(), "", "")
	site, parser, fresh := a.tabs[0], a.tabs[1], a.tabs[2]

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	drag := func(from, to string) {
		t.Helper()
		src, ok := tt.Find(from)
		dst, ok2 := tt.Find(to)
		if !ok || !ok2 {
			t.Fatalf("no %q or %q: %q", from, to, tt.Texts())
		}
		tt.Press(src.X+20, src.Y+src.H/2)
		tt.Move(src.X+40, src.Y+src.H/2)
		tt.Move(dst.X+20, dst.Y+dst.H/2)
		tt.Release(dst.X+20, dst.Y+dst.H/2)
		tt.Frame()
	}
	drag("Polish the landing copy tab", "New session tab")
	if !slices.Equal(a.tabs, []*tab{parser, fresh, site}) {
		t.Fatalf("dragged right: %v", a.tabs)
	}
	drag("New session tab", "Refactor the parser and run the tests tab")
	if !slices.Equal(a.tabs, []*tab{fresh, parser, site}) {
		t.Fatalf("dragged left: %v", a.tabs)
	}
	if a.focused() != fresh {
		t.Fatal("a drag shouldn't change the tab shown")
	}
	if err := tt.Click("Polish the landing copy tab"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focused() != site {
		t.Fatal("a click on a tab didn't show it")
	}
}

func TestHideSessionsFromTheSidebar(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	mu.Lock() // the window's updates wait for the test
	defer mu.Unlock()
	a.setProjects(index(t, data))
	a.restore()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.RightClick("Polish the landing copy"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Hide from Sidebar"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if tt.HasText("Polish the landing copy") || !tt.HasText("Show 1 hidden") {
		t.Fatalf("hidden: %q", tt.Texts())
	}
	// Search still finds it.
	a.query = "landing"
	tt.Frame()
	if !tt.HasText("Polish the landing copy") {
		t.Fatalf("search: %q", tt.Texts())
	}
	a.query = ""
	tt.Frame()

	// It stays hidden next launch.
	a.saveState()
	a.Close()
	next, _ := serialApp(Options{DataDir: data})
	defer next.Close()
	next.setProjects(index(t, data))
	next.restore()
	tt = ui.NewTester(next.view, 1240, 800)
	tt.Frame()
	if tt.HasText("Polish the landing copy") {
		t.Fatal("not hidden next launch")
	}
	if err := tt.Click("Show 1 hidden"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.RightClick("Polish the landing copy"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Show in Sidebar"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if len(next.hidden) != 0 || tt.HasText("Show 1 hidden") {
		t.Fatalf("shown again: %v, %q", next.hidden, tt.Texts())
	}
}

// The menu bar takes Command keys before the window does on macOS, so it
// must give them the window's meanings: Command+W closes the tab, not the
// window, and no two items share a key.
func TestMenuBar(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	a.selectStart()
	byKey := map[string]command{}
	for _, g := range a.menuBar() {
		for _, cmd := range g.items {
			if cmd.key == "" {
				continue
			}
			if prev, ok := byKey[cmd.key]; ok {
				t.Fatalf("%s is both %q and %q", cmd.key, prev.label, cmd.label)
			}
			byKey[cmd.key] = cmd
		}
	}
	if c := byKey["Cmd+W"]; c.label != "Close Tab" || c.run == nil {
		t.Fatalf("Cmd+W: %+v", c)
	}
	if c := byKey["Shift+Cmd+W"]; c.role != mygo.RoleClose {
		t.Fatalf("Shift+Cmd+W: %+v", c)
	}
	byKey["Cmd+T"].run()
	byKey["Cmd+="].run()
	if len(a.tabs) != 2 || a.fontSize != fontSize+1 {
		t.Fatalf("new session and bigger text: %d tabs, size %v", len(a.tabs), a.fontSize)
	}
	byKey["Cmd+W"].run()
	if len(a.tabs) != 1 {
		t.Fatalf("close tab: %d tabs", len(a.tabs))
	}
}

func TestProjectMenu(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	var copied, opened string
	a.copyText = func(s string) { copied = s }
	a.openPath = func(p string) { opened = p }
	a.setProjects(index(t, data))
	var site session.Project
	for _, p := range a.projects {
		if p.Name == "ornith-page" {
			site = p
		}
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.RightClick("ornith-page"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Path"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if copied != site.Root {
		t.Fatalf("copied %q, want %q", copied, site.Root)
	}
	open := "Open Folder"
	if runtime.GOOS == "darwin" {
		open = "Open in Finder"
	}
	if err := tt.RightClick("ornith-page"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(open); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if opened != site.Root {
		t.Fatalf("opened %q", opened)
	}
	// A session inside the project keeps its own menu.
	if err := tt.RightClick("Polish the landing copy"); err != nil {
		t.Fatal(err)
	}
	if menu := strings.Join(tt.Menu(), ","); !strings.Contains(menu, "Fork into a New Tab") {
		t.Fatalf("session menu: %s", menu)
	}
}

func TestLeavingWhileWorkingAsks(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	var left []bool
	a.leave = func(quit bool) { left = append(left, quit) }
	a.setProjects(index(t, data))
	var site string
	for i, p := range a.projects {
		for j, s := range p.Sessions {
			if s.Title == "Polish the landing copy" {
				site = s.Path
				a.projects[i].Sessions[j].Status = status.Report{State: status.Working}
			}
		}
	}
	a.newTab(t.TempDir(), "", "")
	tt := ui.NewTester(a.view, 1240, 800)

	// Nothing working: the window closes.
	if !a.windowClosing() || a.confirmingLeave {
		t.Fatal("asked with nothing working")
	}
	a.selectSession(site)

	// A quit: asked, cancelled.
	a.quitting = true
	if a.windowClosing() {
		t.Fatal("closed with a session working")
	}
	tt.Frame()
	if !tt.HasText("Quit Larik?") || !tt.HasText("“Polish the landing copy” is working. Leaving stops its turn; the session stays, and its tab opens again next time.") {
		t.Fatalf("quit: %q", tt.Texts())
	}
	snapshot(t, tt, "quit.png")
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.quitting || a.leaving || len(left) != 0 {
		t.Fatalf("cancel: quitting %v, leaving %v, %v", a.quitting, a.leaving, left)
	}

	// The window's close button: asked, and closed.
	a.windowClosing()
	tt.Frame()
	if !tt.HasText("Close the window?") {
		t.Fatalf("close: %q", tt.Texts())
	}
	if err := tt.Click("Close Window"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Equal(left, []bool{false}) || !a.windowClosing() {
		t.Fatalf("closed: %v", left)
	}
}

func TestDockMenu(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	var sets [][]string
	a.dock = func(items []command) {
		var labels []string
		for _, it := range items {
			labels = append(labels, it.label)
		}
		sets = append(sets, labels)
	}
	a.setProjects(index(t, data))
	a.setProjects(index(t, data)) // nothing changed: not set again
	want := []string{"Add OSC 7501 output — needs you", "", "New Session"}
	if len(sets) != 1 || !slices.Equal(sets[0], want) {
		t.Fatalf("dock menus: %q", sets)
	}
	var blocked string
	for _, s := range a.waiting() {
		blocked = s.Path
	}
	a.docked[0].run() // opens the session (it runs elsewhere, so says so)
	if a.notice == "" && (a.focused() == nil || a.focused().path != blocked) {
		t.Fatal("the Dock item didn't open its session")
	}
}

func TestChangesFollowTheMainTab(t *testing.T) {
	needTerminal(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t)})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	one, two := t.TempDir(), t.TempDir()
	a.newTab(one, "", "")
	first := shownAt(a, 0)
	a.startTab(two, "", "")
	second := a.tabs[1]
	a.toggleChanges()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if a.changes == nil || a.changes.dir != one {
		t.Fatalf("changes for %v", a.changes)
	}
	watcher := a.changes.stop
	a.activate(second)
	tt.Frame()
	if a.changes == nil || a.changes.dir != two {
		t.Fatalf("after switching tabs, changes for %v", a.changes)
	}
	select {
	case <-watcher:
	default:
		t.Fatal("the old changes' watcher still runs")
	}
	a.closeTab(second)
	a.closeTab(first)
	tt.Frame()
	if a.changes != nil {
		t.Fatal("the changes stayed open with no tab")
	}
}

func TestStateWritesLandInOrder(t *testing.T) {
	data := t.TempDir()
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	mu.Lock()
	a.restore()
	for i := range 40 {
		a.sidebarW = float32(200 + i)
		a.saveState()
	}
	mu.Unlock()
	a.Close() // waits for every write
	if saved, _ := os.ReadFile(a.statePath()); !strings.Contains(string(saved), `"sidebar":239`) {
		t.Fatalf("an older state landed last: %s", saved)
	}
}

// A click in a pane's find bar gives its tab the keyboard, so Command+W
// and the rest act on it.
func TestFindBarFocusesItsPane(t *testing.T) {
	needTerminal(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t)})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.newTab(t.TempDir(), "", "")
	left := shownAt(a, 0)
	left.finding = true
	a.splitRight()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	tt.Frame()
	if a.focused() != shownAt(a, 1) {
		t.Fatal("the new split should have the keyboard")
	}
	if err := tt.Click("Find in the terminal"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focused() != left {
		t.Fatal("a click in the left pane's find bar left the split focused")
	}
}

func TestOneWindowPerDataDir(t *testing.T) {
	data := t.TempDir()
	if Handover(data, "/somewhere") {
		t.Fatal("handed over with no window open")
	}
	got := make(chan string, 4)
	ln, err := listen(data, func(cwd string) { got <- cwd }, noControl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listen(data, func(string) {}, noControl); !errors.Is(err, errRunning) {
		t.Fatalf("a second window: %v", err)
	}
	if !Handover(data, "/work/site") {
		t.Fatal("no window took the directory")
	}
	// The second window's check came first, empty.
	var seen []string
	for len(seen) < 2 {
		select {
		case cwd := <-got:
			seen = append(seen, cwd)
		case <-time.After(2 * time.Second):
			t.Fatalf("handed over: %q", seen)
		}
	}
	if !slices.Contains(seen, "/work/site") {
		t.Fatalf("handed over: %q", seen)
	}
	// Another data directory has a window of its own.
	if Handover(t.TempDir(), "/x") {
		t.Fatal("handed to another data directory's window")
	}
	ln.Close()
	if Handover(data, "/after") {
		t.Fatal("handed over after the window closed")
	}
	// A socket left by a window that crashed is taken over.
	os.WriteFile(socketPath(data), nil, 0o600)
	ln, err = listen(data, func(string) {}, noControl)
	if err != nil {
		t.Fatalf("over a stale socket: %v", err)
	}
	ln.Close()
}

func TestOpenDirShowsItsProject(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	site := filepath.Join(filepath.Dir(repo), "ornith-page")
	a.openDir(site)
	if shownAt(a, 0) == nil || filepath.Base(shownAt(a, 0).dir) != "ornith-page" || a.tabTitle(shownAt(a, 0)) != "Polish the landing copy" {
		t.Fatalf("open the site: %v", shownAt(a, 0))
	}
	elsewhere := t.TempDir()
	a.openDir(elsewhere)
	if shownAt(a, 0).dir != elsewhere || shownAt(a, 0).path != "" {
		t.Fatalf("a directory with no sessions gets a new one: %+v", shownAt(a, 0))
	}
	home, _ := os.UserHomeDir()
	n := len(a.tabs)
	a.openDir(home)
	if len(a.tabs) != n {
		t.Fatal("the home directory opened a tab")
	}
}

func TestReopenClosedTabs(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	paths := map[string]string{}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			paths[s.Title] = s.Path
		}
	}
	site, parser := paths["Polish the landing copy"], paths["Refactor the parser and run the tests"]
	a.selectSession(site)
	a.selectSession(parser)
	a.newTab(t.TempDir(), "", "") // nothing to reopen: no session yet
	a.closeTab(a.tabOf(site))
	a.closeTab(a.tabOf(parser))
	a.closeTab(shownAt(a, 0))
	if len(a.tabs) != 0 || !slices.Equal(a.closed, []string{site, parser}) {
		t.Fatalf("closed: %d tabs, %q", len(a.tabs), a.closed)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if runtime.GOOS == "darwin" {
		tt.Key(ui.Super|ui.Shift, ui.KeyT)
	} else {
		a.reopenClosed()
	}
	tt.Frame()
	if shownAt(a, 0) == nil || shownAt(a, 0).path != parser {
		t.Fatalf("reopened first: %v", shownAt(a, 0))
	}
	if err := tt.RightClick(a.tabTitle(shownAt(a, 0)) + " tab"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Reopen Closed Tab"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if a.focused() == nil || a.focused().path != site || len(a.tabs) != 2 {
		t.Fatalf("reopened second: %d tabs, %v", len(a.tabs), a.focused())
	}
	a.reopenClosed()
	if a.notice != "No closed tab to reopen." {
		t.Fatalf("nothing left: %q", a.notice)
	}
}

func TestArrowKeysPickASearchMatch(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("Search sessions (" + keyLabel(false, "K") + ")"); err != nil {
		t.Fatal(err)
	}
	tt.Type("the")
	tt.Frame()
	found := a.matches()
	if len(found) != 2 {
		t.Fatalf("matches: %d", len(found))
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown) // stays on the last
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.pick != 1 {
		t.Fatalf("picked %d", a.pick)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if shownAt(a, 0) == nil || shownAt(a, 0).path != found[1].Path || a.query != "" || a.pick != 0 {
		t.Fatalf("opened %v, query %q", shownAt(a, 0), a.query)
	}
}

func TestOpenFolderAndDrops(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()

	// Open Folder asks for one, starting in the current directory.
	picked := t.TempDir()
	var start string
	a.pickDir = func(from string, then func(string)) { start = from; then(picked) }
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	tt.Key(mod, ui.KeyO)
	tt.Frame()
	if shownAt(a, 0) == nil || shownAt(a, 0).dir != picked || start != a.opts.Cwd {
		t.Fatalf("opened %v, asked from %q", shownAt(a, 0), start)
	}

	// A folder, and the folder of a file, dropped on the sidebar; a path
	// that's gone is left out.
	dropped, other := t.TempDir(), t.TempDir()
	file := filepath.Join(other, "notes.md")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n := len(a.tabs)
	if !tt.DropFiles(100, 400, dropped, file, filepath.Join(other, "gone"), other) {
		t.Fatal("the sidebar didn't take the drop")
	}
	tt.Frame()
	if len(a.tabs) != n+2 || a.tabs[n].dir != dropped || shownAt(a, 0).dir != other {
		t.Fatalf("after the drop: %d tabs (had %d), showing %v", len(a.tabs), n, shownAt(a, 0))
	}
}

func TestRestartAFailedTab(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	var site session.Indexed
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Title == "Polish the landing copy" {
				site = s
			}
		}
	}
	a.newTab(t.TempDir(), "", "")
	a.selectSession(site.Path)
	tb := shownAt(a, 0)
	first := tb.term
	mu.Unlock()

	first.Send([]byte("3\r"))
	waitFor(t, mu, "failed tab", func() bool { return tb.exited })
	mu.Lock()
	defer mu.Unlock()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("larik exited with status 3; restarting resumes the session") {
		t.Fatalf("no exit bar: %q", tt.Texts())
	}
	if err := tt.Click("Restart"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.exited || tb.term == first || len(a.tabs) != 2 || a.tabs[1] != tb || shownAt(a, 0) != tb {
		t.Fatalf("restarted: exited %v, %d tabs, showing %v", tb.exited, len(a.tabs), shownAt(a, 0))
	}
	if tt.HasText("Restart") {
		t.Error("the exit bar stays after the restart")
	}
	mu.Unlock()
	waitFor(t, mu, "larik resumed", func() bool {
		return strings.Contains(tb.term.Text(), "args:--resume "+site.ID+" tab:"+tb.id)
	})
	// The first larik's end, already handled, doesn't end the new one.
	mu.Lock()
	if tb.exited {
		t.Error("the old terminal's exit reached the restarted tab")
	}

	// The tab menu restarts one too.
	mu.Unlock()
	tb.term.Send([]byte("4\r"))
	waitFor(t, mu, "failed again", func() bool { return tb.exited })
	mu.Lock()
	second := tb.term
	tt.Frame()
	if err := tt.RightClick(a.tabTitle(tb) + " tab"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Restart"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if tb.exited || tb.term == second {
		t.Fatal("the tab menu didn't restart it")
	}
}

func TestDragSessionsOntoPanes(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	var titles []string
	a.setTitle = func(s string) { titles = append(titles, s) }
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	site, ok1 := tt.Find("Polish the landing copy")
	parser, ok2 := tt.Find("Refactor the parser and run the tests")
	if !ok1 || !ok2 {
		t.Fatalf("sessions not in the sidebar: %q", tt.Texts())
	}
	drag := func(from ui.Rect, x, y float32) {
		tt.Press(from.X+20, from.Y+from.H/2)
		tt.Move(from.X+40, from.Y+from.H/2)
		tt.Move(x, y)
		tt.Release(x, y)
		tt.Frame()
	}
	if !slices.Equal(titles, []string{"Larik"}) {
		t.Fatalf("titles at first: %q", titles)
	}

	// Onto the empty main pane.
	drag(site, 700, 450)
	if shownAt(a, 0) == nil || a.tabTitle(shownAt(a, 0)) != "Polish the landing copy" {
		t.Fatalf("after the first drop: %v", shownAt(a, 0))
	}
	siteTab := shownAt(a, 0)
	if titles[len(titles)-1] != "Polish the landing copy — Larik" {
		t.Errorf("title: %q", titles)
	}

	// Onto the split button: it opens in the split.
	split, ok := tt.Find(splitTip())
	if !ok {
		t.Fatal("no split button")
	}
	drag(parser, split.X+split.W/2, split.Y+split.H/2)
	if shownAt(a, 1) == nil || a.tabTitle(shownAt(a, 1)) != "Refactor the parser and run the tests" || shownAt(a, 0) != siteTab {
		t.Fatalf("after the split drop: main %v, split %v", shownAt(a, 0), shownAt(a, 1))
	}
	if titles[len(titles)-1] != "Refactor the parser and run the tests — Larik" {
		t.Errorf("the title follows the focused pane: %q", titles)
	}

	// The split's session onto the main pane swaps the two.
	parserTab := shownAt(a, 1)
	drag(parser, 500, 450)
	if shownAt(a, 0) != parserTab || shownAt(a, 1) != siteTab || len(a.tabs) != 2 {
		t.Fatalf("after the swap: main %v, split %v, %d tabs", shownAt(a, 0), shownAt(a, 1), len(a.tabs))
	}
}

func TestPinSessions(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(index(t, data))
	a.restore()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if tt.HasText("Pinned") {
		t.Fatal("a Pinned section with nothing pinned")
	}
	count := func(text string) int {
		n := 0
		for _, s := range tt.Texts() {
			if s == text {
				n++
			}
		}
		return n
	}
	pin := func(title, item string) {
		t.Helper()
		if err := tt.RightClick(title); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(item); err != nil {
			t.Fatal(err, tt.Menu())
		}
		tt.Frame()
	}
	pin("Polish the landing copy", "Pin to Top")
	pin("Refactor the parser and run the tests", "Pin to Top")
	if !tt.HasText("Pinned") || count("Polish the landing copy") != 1 {
		t.Fatalf("pinned: %q", tt.Texts())
	}
	// Above every project, in the order pinned.
	pinned, _ := tt.Find("Pinned")
	site, _ := tt.Find("Polish the landing copy")
	parser, _ := tt.Find("Refactor the parser and run the tests")
	if first, ok := tt.Find("New session here"); !ok || first.Y < parser.Y {
		t.Errorf("a project above the pinned sessions: %v", first)
	}
	if !(pinned.Y < site.Y && site.Y < parser.Y) {
		t.Errorf("order: Pinned %v, site %v, parser %v", pinned.Y, site.Y, parser.Y)
	}

	// Hiding a pinned session unpins it.
	pin("Refactor the parser and run the tests", "Hide from Sidebar")
	if s, _, _ := a.find(a.pinned[0]); len(a.pinned) != 1 || s.Title != "Polish the landing copy" {
		t.Fatalf("hiding didn't unpin: %q", a.pinned)
	}

	// Pinned next launch.
	a.saveState()
	a.Close()
	next, _ := serialApp(Options{DataDir: data})
	defer next.Close()
	next.setProjects(index(t, data))
	next.restore()
	tt = ui.NewTester(next.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Pinned") || count("Polish the landing copy") != 1 {
		t.Fatalf("not pinned next launch: %q", tt.Texts())
	}
	pin("Polish the landing copy", "Unpin")
	if len(next.pinned) != 0 || tt.HasText("Pinned") || count("Polish the landing copy") != 1 {
		t.Fatalf("unpinned: %q, %q", next.pinned, tt.Texts())
	}
}

func TestDragToPin(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(index(t, data))
	a.restore()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	paths := map[string]string{}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			paths[s.Title] = s.Path
		}
	}
	const site, parser = "Polish the landing copy", "Refactor the parser and run the tests"
	// drag drags the session titled from onto the text to, found once
	// the drag has begun.
	drag := func(from, to string) {
		t.Helper()
		r, ok := tt.Find(from)
		if !ok {
			t.Fatalf("no %q: %q", from, tt.Texts())
		}
		tt.Press(r.X+20, r.Y+r.H/2)
		tt.Move(r.X+40, r.Y+r.H/2)
		tt.Frame()
		tt.Frame()
		dst, ok := tt.Find(to)
		if !ok {
			tt.Release(r.X+40, r.Y+r.H/2)
			t.Fatalf("no %q while dragging: %q", to, tt.Texts())
		}
		tt.Move(dst.X+20, dst.Y+dst.H/2)
		tt.Release(dst.X+20, dst.Y+dst.H/2)
		tt.Frame()
	}
	if tt.HasText("Drop here to pin") {
		t.Fatal("the drop zone shows before a drag")
	}
	drag(site, "Drop here to pin")
	if !slices.Equal(a.pinned, []string{paths[site]}) || tt.HasText("Drop here to pin") {
		t.Fatalf("dropped on the zone: %q, %q", a.pinned, tt.Texts())
	}
	// Dropped on a pinned session, before it.
	drag(parser, site)
	if !slices.Equal(a.pinned, []string{paths[parser], paths[site]}) {
		t.Fatalf("dropped on a pinned one: %q", a.pinned)
	}
	// Pinned ones reorder.
	drag(site, parser)
	if !slices.Equal(a.pinned, []string{paths[site], paths[parser]}) {
		t.Fatalf("reordered: %q", a.pinned)
	}
}

func TestRenameSessions(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(index(t, data))
	a.restore()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	const prompt = "Polish the landing copy"
	rename := func(title, name string, key ui.Key) {
		t.Helper()
		if err := tt.RightClick(title); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Rename…"); err != nil {
			t.Fatal(err, tt.Menu())
		}
		tt.Frame()
		tt.Frame()
		if !tt.Focused("Session name") || a.newName != title {
			t.Fatalf("renaming in place: field focused %v, says %q", tt.Focused("Session name"), a.newName)
		}
		snapshot(t, tt, "rename.png")
		tt.Type(name) // over the name, selected as the field opens
		if key != 0 {
			tt.Key(0, key)
		} else if err := tt.Click("Search sessions (" + keyLabel(false, "K") + ")"); err != nil {
			t.Fatal(err) // clicking away
		}
		tt.Frame()
		if a.renaming != "" || tt.Focused("Session name") {
			t.Fatal("still renaming")
		}
	}
	rename(prompt, "  Landing   copy ", ui.KeyEnter)
	if !tt.HasText("Landing copy") || tt.HasText(prompt) {
		t.Fatalf("renamed: %q", tt.Texts())
	}
	path := ""
	for p, n := range a.names {
		if n == "Landing copy" {
			path = p
		}
	}
	// Search finds it by its name and by its first prompt.
	for _, q := range []string{"landing copy", "polish"} {
		a.query = q
		if found := a.matches(); len(found) != 1 || found[0].Path != path {
			t.Errorf("search %q: %v", q, found)
		}
	}
	a.query = ""
	// The name outlives a new index, and Escape changes nothing.
	a.setProjects(index(t, data))
	tt.Frame()
	rename("Landing copy", "Something else", ui.KeyEscape)
	if !tt.HasText("Landing copy") {
		t.Fatalf("after a new index and Escape: %q", tt.Texts())
	}
	// Clicking away keeps what was typed, as Finder does.
	rename("Landing copy", "Landing page copy", 0)
	if !tt.HasText("Landing page copy") {
		t.Fatalf("after clicking away: %q", tt.Texts())
	}
	rename("Landing page copy", "Landing copy", ui.KeyEnter)

	// Named next launch: quitting waits for the state to be written.
	a.saveState()
	a.Close()
	next, _ := serialApp(Options{DataDir: data})
	defer next.Close()
	next.setProjects(index(t, data))
	next.restore()
	a, tt = next, ui.NewTester(next.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Landing copy") {
		t.Fatalf("not named next launch: %q", tt.Texts())
	}
	// A blank name gives the first prompt back.
	rename("Landing copy", "", ui.KeyEnter)
	if !tt.HasText(prompt) || len(next.names) != 0 {
		t.Fatalf("unnamed: %v, %q", next.names, tt.Texts())
	}
}

// In the window, confirmations are the system's sheets, asked once each
// and answered on the window's frame.
func TestConfirmationsAskInSheets(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	var site string
	for i, p := range a.projects {
		for j, s := range p.Sessions {
			if s.Title == "Polish the landing copy" {
				site = s.Path
				a.projects[i].Sessions[j].Status = status.Report{State: status.Working}
			}
		}
	}
	a.selectSession(site)
	type question struct {
		title, yes string
		then       func(bool)
	}
	var asked []question
	a.ask = func(title, msg, yes string, then func(bool)) { asked = append(asked, question{title, yes, then}) }
	left := ""
	a.leave = func(quit bool) { left = map[bool]string{true: "quit", false: "closed"}[quit] }
	tt := ui.NewTester(a.view, 1240, 800)

	a.askClose([]*tab{shownAt(a, 0)}, nil)
	tt.Frame()
	tt.Frame()
	if len(asked) != 1 || asked[0].title != "Close “Polish the landing copy”?" || asked[0].yes != "Close" || tt.HasText("Cancel") {
		t.Fatalf("asked %+v; drawn %q", asked, tt.Texts())
	}
	asked[0].then(false)
	tt.Frame()
	if len(a.tabs) != 1 || a.confirming {
		t.Fatalf("cancelled: %d tabs, confirming %v", len(a.tabs), a.confirming)
	}

	// Quitting while it works.
	a.quitting = true
	if a.windowClosing() {
		t.Fatal("the window closed with a tab working")
	}
	tt.Frame()
	if len(asked) != 2 || asked[1].title != "Quit Larik?" || asked[1].yes != "Quit" {
		t.Fatalf("asked %+v", asked)
	}
	asked[1].then(true)
	if left != "quit" || !a.leaving || !a.windowClosing() {
		t.Fatalf("quit: left %q, leaving %v", left, a.leaving)
	}
}

// The window takes macOS's colours over the system's theme: the accent
// chosen in System Settings, light or dark.
func TestLooksLikeMacOS(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	a.selectStart()
	a.splitRight()
	purple := ui.Hex("#a550a7")
	for _, dark := range []bool{false, true} {
		tt := ui.NewTester(a.view, 1240, 800)
		tt.SetDark(dark)
		tt.SetPreferences(ui.Preferences{Accent: purple})
		tt.Frame()
		th := a.theme[map[bool]int{false: 0, true: 1}[dark]]
		if th == nil || th.Accent != purple || th.Dark != dark {
			t.Fatalf("dark %v: theme %+v", dark, th)
		}
		want := contentLight
		if dark {
			want = contentDark
		}
		if th.Background != want {
			t.Errorf("dark %v: content %v", dark, th.Background)
		}
		snapshot(t, tt, map[bool]string{false: "look-light.png", true: "look-dark.png"}[dark])
	}
}

func TestSplitButton(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	a.selectStart()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click(splitTip()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.panes()) != 2 || len(a.tabs) != 2 || a.layout.below {
		t.Fatalf("split: %d panes, %d tabs", len(a.panes()), len(a.tabs))
	}
	// Right-clicked, it splits the focused pane below.
	if err := tt.RightClick(splitTip()); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("New Session in Split Below"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.panes()) != 3 || len(a.tabs) != 3 || !a.layout.second.below || a.focused() != shownAt(a, 2) {
		t.Fatalf("split below: %d panes, %d tabs", len(a.panes()), len(a.tabs))
	}
	// A pane's close button closes it, and its tab stays.
	first := shownAt(a, 0)
	if err := tt.Click("Close pane (the tab stays open)"); err != nil {
		t.Fatal(err, tt.Texts())
	}
	tt.Frame()
	if len(a.panes()) != 2 || len(a.tabs) != 3 || a.shown(first) || !a.layout.below {
		t.Fatalf("closing a pane: %d panes, %d tabs", len(a.panes()), len(a.tabs))
	}
}

func TestWindowMenuListsTabs(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	sets := 0
	a.setMenu = func() { sets++ }
	var opened []string
	a.openExternal = func(url string) { opened = append(opened, url) }
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if sets != 0 {
		t.Fatalf("the menu bar set again with no tabs: %d", sets)
	}
	a.selectStart()
	first := shownAt(a, 0)
	a.newTab(t.TempDir(), "", "")
	tt.Frame()
	if sets != 1 {
		t.Fatalf("set %d times for two new tabs", sets)
	}
	group := func(title string) []command {
		for _, g := range a.menuBar() {
			if g.title == title {
				return g.items
			}
		}
		t.Fatalf("no %s menu", title)
		return nil
	}
	win := group("Window")
	tabs := win[len(win)-2:]
	if tabs[0].label != a.tabTitle(first) || tabs[0].key != "Cmd+1" || tabs[0].checked ||
		tabs[1].key != "Cmd+2" || !tabs[1].checked {
		t.Fatalf("window menu's tabs: %+v", tabs)
	}
	tabs[0].run()
	tt.Frame()
	if shownAt(a, 0) != first || sets != 2 {
		t.Fatalf("choosing the first: showing %v, set %d times", shownAt(a, 0), sets)
	}
	tt.Frame()
	if sets != 2 {
		t.Fatalf("set again with nothing changed: %d", sets)
	}
	for _, cmd := range group("Help") {
		cmd.run()
	}
	if !slices.Equal(opened, []string{helpURL, guideURL}) {
		t.Fatalf("help opened %q", opened)
	}
}

func TestSettingsOpenInATab(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	var settings command
	for _, g := range a.menuBar() {
		for _, cmd := range g.items {
			if cmd.label == "Settings…" {
				if g.role != mygo.RoleAppMenu || cmd.key != "Cmd+," {
					t.Errorf("Settings… is in %q on %q", g.role, cmd.key)
				}
				settings = cmd
			}
		}
	}
	if settings.run == nil {
		mu.Unlock()
		t.Fatal("no Settings… in the menu bar")
	}
	settings.run()
	tab := a.focused()
	if tab == nil || len(a.tabs) != 1 {
		mu.Unlock()
		t.Fatalf("settings: %d tabs", len(a.tabs))
	}
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "larik /config", func() bool {
		text := tab.term.Text()
		return strings.Contains(text, "args:/config tab:") && strings.Contains(text, filepath.Base(repo))
	})

	// The key opens another, off macOS too.
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	mu.Lock()
	tt.Key(mod, ui.KeyComma)
	tt.Frame()
	n := len(a.tabs)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("the key opened %d tabs", n)
	}
}

func TestAboutShowsTheVersion(t *testing.T) {
	a, _ := serialApp(Options{Version: "9.9.9"})
	defer a.Close()
	var shown *mygo.AboutPanelOptions
	a.about = func(o mygo.AboutPanelOptions) { shown = &o }
	for _, g := range a.menuBar() {
		for _, cmd := range g.items {
			if cmd.label == "About Larik" && g.role == mygo.RoleAppMenu {
				cmd.run()
			}
		}
	}
	if shown == nil || shown.ApplicationName != "Larik" || shown.ApplicationVersion != "9.9.9" {
		t.Fatalf("about: %+v", shown)
	}
}

// noControl answers every request with nothing, for tests of the
// handover alone.
func noControl(Request) Response { return Response{} }

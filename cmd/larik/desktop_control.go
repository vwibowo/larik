package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"larik/internal/config"
	"larik/internal/desktop"
)

// desktopCommands are the commands larik desktop sends the open window,
// with what each does, for usage.
var desktopCommands = []struct{ name, usage, about string }{
	{"list", "", "list the window's tabs: id, session, state, directory and title"},
	{"open", "[--session id] [--prompt text] [--shell | --run cmd] [--worktree [--branch name]] [--split | --below | --background] [dir]", "open a session in a tab, or start one in dir (this directory by default), or with --shell or --run a terminal tab running your shell or cmd there; --worktree starts it in a new git worktree of dir's repository, on the branch larik/name; prints the tab's id"},
	{"send", "--tab tab [--no-enter] [text | -]", "type text into a tab and press Return; - or no text reads it from stdin"},
	{"read", "--tab tab [--lines n]", "print the last lines a tab's terminal shows, its scrollback included"},
	{"wait", "(--tab tab | --session id) [--state done] [--timeout d]", "wait until the session is in a state: done, error, blocked, working or idle, or several comma separated; prints the state"},
	{"allow", "--tab tab [--always]", "answer the permission the tab's session waits for, then print what it does next: yes (for a plan: approve, accepting edits); --always takes the prompt's second option (don't ask again; for a plan: approve, asking before each edit)"},
	{"deny", "--tab tab", "answer the permission the tab's session waits for with no (for a plan: keep planning)"},
	{"focus", "--tab tab", "show a tab and bring the window forward"},
	{"close", "--tab tab [--force]", "close a tab; --force stops one mid-turn"},
	{"notify", "[--title title] text", "post a notification"},
	{"archive", "--session id", "archive a session that isn't running: it leaves the sidebar and /resume for its project's archived ones"},
	{"unarchive", "--session id", "bring an archived session back"},
	{"trash", "--session id", "move a session that isn't running, archived or not, to the Trash, with what larik kept beside it"},
}

// isDesktopCommand reports whether name is one of desktopCommands.
func isDesktopCommand(name string) bool {
	for _, c := range desktopCommands {
		if c.name == name {
			return true
		}
	}
	return false
}

// desktopUsage lists the commands, for larik desktop -h.
func desktopUsage() string {
	var b strings.Builder
	b.WriteString("Commands for the open window, for scripts and for agents in its tabs\n(a tab's own id is in $LARIK_DESKTOP_TAB; --tab takes it, the id of its\nsession or enough of one to tell, or \"focused\"; --json prints the answer\nas JSON):\n\n")
	for _, c := range desktopCommands {
		fmt.Fprintf(&b, "  %s\n      %s\n", strings.TrimSpace("larik desktop "+c.name+" "+c.usage), c.about)
	}
	b.WriteString("\nWith the window closed, its tabs go on in the background, and list, read,\nsend, close and wait reach them there. And for what runs there:\n\n")
	for _, c := range hostCommands {
		fmt.Fprintf(&b, "  %s\n      %s\n", strings.TrimSpace("larik desktop host "+c.name+" "+c.usage), c.about)
	}
	b.WriteString("\n")
	return b.String()
}

// runDesktopCommand sends the open window the command name, with args.
func runDesktopCommand(name string, args []string, stdin io.Reader, w io.Writer) error {
	fs := flag.NewFlagSet("desktop "+name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the window's answer as JSON")
	req := desktop.Request{Cmd: name}
	var noEnter bool
	switch name {
	case "open":
		fs.StringVar(&req.Session, "session", "", "the session to open, by id or the start of one")
		fs.StringVar(&req.Prompt, "prompt", "", "what the session starts with")
		fs.BoolVar(&req.Split, "split", false, "open it in a new pane beside the focused one")
		fs.BoolVar(&req.Below, "below", false, "open it in a new pane below the focused one")
		fs.BoolVar(&req.Background, "background", false, "open it in a tab without showing it")
		fs.BoolVar(&req.Shell, "shell", false, "open a terminal tab running your shell instead of larik")
		fs.StringVar(&req.Run, "run", "", "open a terminal tab running this command through your shell, as lazygit or npm run dev")
		fs.BoolVar(&req.Worktree, "worktree", false, "start it in a new git worktree of the directory's repository, apart from your checkout")
		fs.StringVar(&req.Branch, "branch", "", "the worktree's branch, under larik/ (one is named for it when empty); implies --worktree")
	case "send", "read", "focus", "close", "wait", "allow", "deny":
		fs.StringVar(&req.Tab, "tab", "", "the tab: its id, its session's id, or \"focused\"")
	case "archive", "unarchive", "trash":
		fs.StringVar(&req.Session, "session", "", "the session, by id or the start of one")
	}
	switch name {
	case "send":
		fs.BoolVar(&noEnter, "no-enter", false, "type the text without pressing Return")
	case "read":
		fs.IntVar(&req.Lines, "lines", 50, "how many of the last lines to print, 0 for all")
	case "wait":
		fs.StringVar(&req.Session, "session", "", "a session running outside the window, by id")
		fs.StringVar(&req.State, "state", "done", "the state, or states comma separated: done, error, blocked, working, idle")
		fs.DurationVar(&req.Timeout, "timeout", 0, "give up after this long, as 10m (0 waits as long as it takes)")
	case "close":
		fs.BoolVar(&req.Force, "force", false, "close it even mid-turn")
	case "allow":
		fs.BoolVar(&req.Always, "always", false, "the prompt's second option: don't ask again, or for a plan, approve and ask before each edit")
	case "notify":
		fs.StringVar(&req.Title, "title", "Larik", "the notification's title")
	}
	for _, c := range desktopCommands {
		if c.name == name {
			fs.Usage = func() {
				fmt.Fprintf(fs.Output(), "Usage: larik desktop %s %s\n\n%s.\n\n", c.name, c.usage, strings.ToUpper(c.about[:1])+c.about[1:])
				fs.PrintDefaults()
			}
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	words := fs.Args()
	if name == "open" && len(words) > 1 {
		// Flags after the directory too, as larik desktop open . --split.
		if err := fs.Parse(words[1:]); err != nil {
			return err
		}
		words = append(words[:1:1], fs.Args()...)
	}
	rest := strings.Join(words, " ")
	switch name {
	case "open":
		req.Worktree = req.Worktree || req.Branch != "" // a branch says which worktree
		if req.Session == "" {
			dir := rest
			if dir == "" {
				var err error
				if dir, err = os.Getwd(); err != nil {
					return err
				}
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			req.Cwd = abs
		}
	case "send":
		req.Text, req.Enter = rest, !noEnter
		if rest == "" || rest == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			req.Text = strings.TrimSuffix(string(data), "\n")
		}
	case "notify":
		req.Body = rest
	case "wait":
		if req.Tab == "" && req.Session == "" {
			return errors.New("wait needs --tab or --session")
		}
	case "archive", "unarchive", "trash":
		if req.Session == "" {
			return fmt.Errorf("%s needs --session", name)
		}
	}
	res, err := desktop.Call(config.DataDir(), req)
	if *asJSON && (err == nil || res.Error != "") {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(res); encErr != nil {
			return encErr
		}
		return err
	}
	if err != nil {
		return err
	}
	switch name {
	case "list":
		if len(res.Tabs) == 0 {
			fmt.Fprintln(w, "no tabs open")
		}
		home, _ := os.UserHomeDir()
		for _, t := range res.Tabs {
			mark := " "
			if t.Focused {
				mark = "*"
			}
			fmt.Fprintf(w, "%s %s  %-8s  %-7s  %s  %s\n", mark, t.ID, short(t.Session, 8), t.State, tildePath(t.Dir, home), t.Title)
		}
	case "open":
		fmt.Fprintln(w, res.Tab.ID)
	case "read":
		fmt.Fprintln(w, res.Text)
	case "wait", "allow", "deny":
		fmt.Fprintln(w, res.State)
	}
	return nil
}

// short is s's first n characters.
func short(s string, n int) string {
	if s == "" {
		return "-"
	}
	return s[:min(len(s), n)]
}

// hostCommands are larik desktop host's, for what runs in the background.
var hostCommands = []struct{ name, usage, about string }{
	{"list", "", "list what the host runs, with how long each has run and whether the window shows it"},
	{"stop", "[--force]", "end every program the host runs, and the host; --force when any still runs"},
}

// runDesktopHost runs larik desktop host list or stop.
func runDesktopHost(args []string, w io.Writer) error {
	name := args[0]
	fs := flag.NewFlagSet("desktop host "+name, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print it as JSON")
	force := fs.Bool("force", false, "end the programs even while some still run")
	known := false
	for _, c := range hostCommands {
		if c.name == name {
			known = true
			fs.Usage = func() {
				fmt.Fprintf(fs.Output(), "Usage: larik desktop host %s %s\n\n%s.\n\n", c.name, c.usage, strings.ToUpper(c.about[:1])+c.about[1:])
				fs.PrintDefaults()
			}
		}
	}
	if !known {
		return fmt.Errorf("larik desktop host: unknown command %q: list or stop", name)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dataDir := config.DataDir()
	pid, err := desktop.HostPID(dataDir)
	if name == "stop" && err != nil {
		// One that doesn't answer may still be there, by its pid.
		return desktop.StopHost(dataDir)
	}
	if err != nil {
		return err
	}
	tabs, err := desktop.HostTabs(dataDir)
	if err != nil {
		return err
	}
	running := 0
	for _, t := range tabs {
		if !t.Exited {
			running++
		}
	}
	if name == "stop" {
		if running > 0 && !*force {
			return fmt.Errorf("the host runs %s: larik desktop host stop --force ends them (larik desktop host list says which)", programs(running))
		}
		if err := desktop.StopHost(dataDir); err != nil {
			return err
		}
		fmt.Fprintf(w, "stopped the host (pid %d), ending %s\n", pid, programs(running))
		return nil
	}
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			PID  int               `json:"pid"`
			Tabs []desktop.HostTab `json:"tabs"`
		}{pid, tabs})
	}
	fmt.Fprintf(w, "host pid %d, %d running\n", pid, running)
	home, _ := os.UserHomeDir()
	for _, t := range tabs {
		state := t.State
		if t.Exited {
			state = fmt.Sprintf("exit %d", t.Code)
		}
		shown := " "
		if t.Windows > 0 {
			shown = "*" // the window shows it
		}
		fmt.Fprintf(w, "%s %s  %-8s  %-7s  %6s  %s  %s\n", shown, t.ID, short(t.Session, 8), state, age(time.Since(t.Started)), tildePath(t.Dir, home), cmp.Or(t.Run, t.Title))
	}
	return nil
}

// programs is n programs, in words.
func programs(n int) string {
	if n == 1 {
		return "1 program"
	}
	return fmt.Sprintf("%d programs", n)
}

// age is d, roughly: 40s, 12m, 3h, 2d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

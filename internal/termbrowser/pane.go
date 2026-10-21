package termbrowser

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// A terminal or multiplexer that can open a pane beside Larik's and run a
// command in it. Each one is asked through its own command-line tool;
// none needs anything installed beyond the terminal itself.
type splitter struct {
	name string
	// args returns the command that splits the current pane and runs
	// argv in the new one, or nil when this terminal isn't in use.
	args func(env func(string) string, argv []string, size int) []string
}

// splitters are tried in order: a multiplexer before the terminal it runs
// in, since the split has to happen where Larik's pane is.
var splitters = []splitter{
	{"tmux", func(env func(string) string, argv []string, size int) []string {
		if env("TMUX") == "" {
			return nil
		}
		args := []string{"tmux", "split-window", "-h", "-d", "-l", fmt.Sprintf("%d%%", size)}
		if p := env("TMUX_PANE"); p != "" {
			args = append(args, "-t", p)
		}
		return append(args, shellJoin(argv))
	}},
	{"cmux", func(env func(string) string, argv []string, size int) []string {
		if env("CMUX_SOCKET_PATH") == "" && env("CMUX_SURFACE_ID") == "" {
			return nil
		}
		args := []string{"cmux", "new-split", "right", "--focus", "false", "--command", shellJoin(argv)}
		if s := env("CMUX_SURFACE_ID"); s != "" {
			args = append(args, "--surface", s)
		}
		return args
	}},
	{"kitty", func(env func(string) string, argv []string, size int) []string {
		if env("KITTY_WINDOW_ID") == "" {
			return nil
		}
		args := []string{"kitty", "@", "launch", "--location=vsplit", "--keep-focus", "--cwd=current"}
		if p := env("KITTY_WINDOW_ID"); p != "" {
			args = append(args, "--next-to", "id:"+p)
		}
		return append(append(args, "--"), argv...)
	}},
	{"wezterm", func(env func(string) string, argv []string, size int) []string {
		p := env("WEZTERM_PANE")
		if p == "" {
			return nil
		}
		return append([]string{"wezterm", "cli", "split-pane", "--right", "--percent", fmt.Sprint(size), "--pane-id", p, "--"}, argv...)
	}},
}

// errNoSplit means no terminal Larik knows how to split is in use.
var errNoSplit = errors.New("this terminal has no pane-splitting command Larik knows")

// openPane runs argv in a new pane to the right of Larik's, taking size
// percent of the width, and names the terminal that opened it.
func openPane(ctx context.Context, env func(string) string, argv []string, size int, run func(ctx context.Context, args []string) error) (string, error) {
	for _, s := range splitters {
		args := s.args(env, argv, size)
		if args == nil {
			continue
		}
		if err := run(ctx, args); err != nil {
			return s.name, fmt.Errorf("%s could not split the pane: %w", s.name, err)
		}
		return s.name, nil
	}
	return "", errNoSplit
}

// runCommand runs a splitting command, keeping what it printed for the
// error.
func runCommand(ctx context.Context, args []string) error {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil && len(out) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}

// shellJoin quotes argv as one POSIX shell command line.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

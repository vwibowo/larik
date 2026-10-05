package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

type projectInfo struct {
	root, name, branch    string
	files, added, deleted int
	ahead, behind         int
	clean, available      bool
	err                   string
}

type projectInfoMsg struct{ info projectInfo }

type turnStats struct {
	ModelTime              time.Duration
	ToolTime               time.Duration
	TTFTTotal              time.Duration
	TTFTCount              int
	Steps                  int
	Compactions            int
	CompactionMeasurements int
	CompactionSavedTokens  int
}

func projectRootFor(cwd string) string {
	if root := agent.GitRoot(cwd); root != "" {
		return root
	}
	return cwd
}

func (m *model) refreshProject() tea.Cmd {
	if m.projectLoading || m.projectRoot == "" {
		return nil
	}
	m.projectLoading = true
	root := m.projectRoot
	return func() tea.Msg { return projectInfoMsg{info: readProjectInfo(root)} }
}

// readProjectInfo gathers local-only Git status. It never contacts an upstream.
func readProjectInfo(root string) projectInfo {
	p := projectInfo{root: root, name: filepath.Base(root)}
	if root == "" {
		return p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v2", "--branch", "--untracked-files=all")
	status.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := status.Output()
	if err != nil {
		p.err = err.Error()
		return p
	}
	p.available = true
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			p.branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(f) == 2 {
				p.ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				p.behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "), strings.HasPrefix(line, "? "):
			p.files++
		}
	}
	p.clean = p.files == 0
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	diffCmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--numstat", "HEAD")
	diffCmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	diff, err := diffCmd.Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(diff)), "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 {
				continue
			}
			if n, e := strconv.Atoi(f[0]); e == nil {
				p.added += n
			}
			if n, e := strconv.Atoi(f[1]); e == nil {
				p.deleted += n
			}
		}
	}
	return p
}

func (p projectInfo) changeSummary() string {
	if !p.available {
		return "not a git repository"
	}
	if p.clean {
		return "working tree clean"
	}
	return fmt.Sprintf("%d files · +%d / −%d", p.files, p.added, p.deleted)
}

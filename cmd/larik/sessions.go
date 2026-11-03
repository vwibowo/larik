package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"larik/internal/config"
	"larik/internal/session"
	"larik/internal/status"
)

// runSessions implements `larik sessions`: every project's sessions,
// grouped by the directory they ran in, with what each is doing now.
func runSessions(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	all := fs.Bool("all", false, "include projects under the system temp folder")
	asJSON := fs.Bool("json", false, "print the whole index as JSON")
	limit := fs.Int("limit", 5, "sessions to show per project (0 for all); --json always has all")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: larik sessions [--all] [--json] [--limit n]\n\nLists sessions from every project, newest project first, with their status.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	projects, err := session.Index(config.DataDir(), session.IndexOptions{Scratch: *all})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(jsonProjects(projects))
	}
	home, _ := os.UserHomeDir()
	for i, p := range projects {
		if i > 0 {
			fmt.Fprintln(w)
		}
		note := ""
		if p.Missing {
			note = "  (folder gone)"
		}
		fmt.Fprintf(w, "%s  %s%s\n", p.Name, tildePath(p.Root, home), note)
		for j, s := range p.Sessions {
			if *limit > 0 && j == *limit {
				fmt.Fprintf(w, "  … %d more\n", len(p.Sessions)-j)
				break
			}
			title := s.Title
			if s.Subdir != "" {
				title = "[" + s.Subdir + "] " + title
			}
			fmt.Fprintf(w, "  %s %s  %s  %-7s  %s\n", statusMark(s.Status), s.ID, s.Modified.Format("2006-01-02 15:04"), s.Status.State, title)
		}
	}
	return nil
}

// statusMark is a one-character status, as a terminal tab would show it.
func statusMark(r status.Report) string {
	switch r.State {
	case status.Working:
		return "●"
	case status.Blocked:
		return "⏸"
	case status.Done:
		return "✓"
	case status.Error:
		return "✕"
	}
	return "·"
}

func tildePath(p, home string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return filepath.Join("~", rel)
		}
	}
	return p
}

type jsonProject struct {
	Root     string        `json:"root"`
	Name     string        `json:"name"`
	Missing  bool          `json:"missing,omitempty"`
	Scratch  bool          `json:"scratch,omitempty"`
	Updated  time.Time     `json:"updated"`
	Sessions []jsonSession `json:"sessions"`
}

type jsonSession struct {
	ID       string        `json:"id"`
	Path     string        `json:"path"`
	Title    string        `json:"title,omitempty"`
	Cwd      string        `json:"cwd"`
	Subdir   string        `json:"subdir,omitempty"`
	ForkOf   string        `json:"fork_of,omitempty"`
	Modified time.Time     `json:"modified"`
	Status   status.Report `json:"status"`
	Running  bool          `json:"running"`
	Usage    status.Usage  `json:"usage,omitzero"`
}

func jsonProjects(ps []session.Project) []jsonProject {
	out := make([]jsonProject, 0, len(ps))
	for _, p := range ps {
		jp := jsonProject{Root: p.Root, Name: p.Name, Missing: p.Missing, Scratch: p.Scratch, Updated: p.Updated, Sessions: make([]jsonSession, 0, len(p.Sessions))}
		for _, s := range p.Sessions {
			jp.Sessions = append(jp.Sessions, jsonSession{ID: s.ID, Path: s.Path, Title: s.Title, Cwd: s.Cwd, Subdir: s.Subdir, ForkOf: s.ForkOf, Modified: s.Modified, Status: s.Status, Running: s.Running, Usage: s.Usage})
		}
		out = append(out, jp)
	}
	return out
}

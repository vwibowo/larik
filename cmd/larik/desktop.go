package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"larik/internal/config"
	"larik/internal/desktop"
)

// runDesktop implements `larik desktop`: a window with every project's
// sessions. It starts the window in a process of its own and returns, so
// the terminal is free again; --foreground keeps it attached.
func runDesktop(args []string) error {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "keep the window attached to this terminal")
	install := fs.Bool("install", false, "add Larik to ~/Applications (macOS) or the applications menu (Linux), to open it without a terminal")
	uninstall := fs.Bool("uninstall", false, "remove what --install added")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: larik desktop [--foreground | --install | --uninstall]\n\nOpens a window with sessions from every project, grouped by the directory\nthey ran in, and a terminal tab running larik for each open session. New\nsessions start in this directory. The tabs get this shell's environment,\nso provider keys set here work there.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	switch {
	case *uninstall:
		removed, err := desktop.Uninstall()
		for _, p := range removed {
			fmt.Println("removed", p)
		}
		if err == nil && len(removed) == 0 {
			fmt.Println("nothing to remove")
		}
		return err
	case *install:
		where, err := desktop.Install(exe)
		if err != nil {
			return err
		}
		fmt.Printf("installed %s, which runs %s\n", where, exe)
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// One window per data directory: an open one comes forward on this
	// directory instead.
	if desktop.Handover(config.DataDir(), cwd) {
		fmt.Println("larik desktop is already open: showing this directory there")
		return nil
	}
	// Each tab is a terminal, which needs Ghostty's terminal library: the
	// first run downloads it, and a failure shows here rather than in the
	// detached window's log.
	if err := desktop.Prepare(); err != nil {
		return err
	}
	if *foreground {
		return desktop.Run(desktop.Options{Bin: exe, DataDir: config.DataDir(), Cwd: cwd, Version: version})
	}

	logDir := filepath.Join(config.DataDir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "desktop.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "desktop", "--foreground")
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Printf("larik desktop is open (log: %s)\n", logPath)
	return cmd.Process.Release()
}

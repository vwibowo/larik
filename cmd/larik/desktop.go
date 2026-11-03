package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"larik/internal/config"
	"larik/internal/desktop"
)

// runDesktop implements `larik desktop`: a window with every project's
// sessions. It starts the window in a process of its own and returns, so
// the terminal is free again; --foreground keeps it attached.
func runDesktop(args []string) error {
	if len(args) > 0 && isDesktopCommand(args[0]) {
		return runDesktopCommand(args[0], args[1:], os.Stdin, os.Stdout)
	}
	// The host the window's tabs run in, which the window starts: not for
	// running by hand.
	if len(args) == 1 && args[0] == "host" {
		return desktop.RunHost(config.DataDir())
	}
	if len(args) > 1 && args[0] == "host" {
		return runDesktopHost(args[1:], os.Stdout)
	}
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "keep the window attached to this terminal")
	install := fs.Bool("install", false, "add Larik to ~/Applications (macOS) or the applications menu (Linux), to open it without a terminal")
	uninstall := fs.Bool("uninstall", false, "remove what --install added")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: larik desktop [--foreground | --install | --uninstall]\n       larik desktop <command> [flags]   (list, open, send, read, wait, allow, deny, focus, close, notify,\n                                     archive, unarchive, trash)\n       larik desktop host (list | stop [--force])\n\nOpens a window with sessions from every project, grouped by the directory\nthey ran in, and a terminal tab running larik for each open session. New\nsessions start in this directory. The tabs get this shell's environment,\nso provider keys set here work there.\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\n%s", desktopUsage())
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
		opts := desktop.Options{Bin: exe, DataDir: config.DataDir(), Cwd: cwd, Version: version}
		// Tabs whose programs go on when the window closes (see
		// docs/desktop.md), unless LARIK_DESKTOP_HOST=0; not on Windows,
		// whose terminals the host hasn't been tried with.
		if os.Getenv("LARIK_DESKTOP_HOST") != "0" && runtime.GOOS != "windows" {
			opts.Host, opts.StartHost = true, func() error { return startDesktopHost(exe) }
		}
		return desktop.Run(opts)
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

// startDesktopHost starts the host the window's tabs run in, in a process
// of its own, which outlives the window's, logging to the logs folder.
func startDesktopHost(exe string) error {
	logDir := filepath.Join(config.DataDir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(logDir, "desktop-host.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "desktop", "host")
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"larik/internal/browser"
	"larik/internal/config"
	"larik/internal/session"
	"larik/internal/trace"
	"larik/internal/trace/viewer"
)

// runTrace implements `larik trace`: review a session's debug trace in
// the browser, or export it as one HTML file.
func runTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	var (
		port   = fs.Int("port", 0, "port for the viewer on 127.0.0.1 (default: a free one)")
		html   = fs.String("html", "", "write a self-contained HTML page to this file instead of serving")
		noOpen = fs.Bool("no-open", false, "print the URL without opening a browser")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik trace [flags] [session-id | trace directory]\n\n"+
			"Shows a session's debug trace (recorded with --debug, LARIK_DEBUG=1 or /debug on) in the\n"+
			"browser: a timeline of requests, tools and permissions, every request as sent, and the raw\n"+
			"HTTP exchanges. Without an argument it shows the latest traced session in this directory.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	dir, title, err := findTrace(fs.Arg(0))
	if err != nil {
		return err
	}
	opts := viewer.Options{Dir: dir, Title: title}
	if *html != "" {
		f, err := os.OpenFile(*html, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if err := viewer.Export(opts, f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Println("wrote", *html)
		return nil
	}
	srv, err := viewer.Start(opts, *port)
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Printf("Trace of %s\n  %s\n(ctrl+c to stop)\n", title, srv.URL())
	if !*noOpen {
		browser.Open(srv.URL())
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}

// findTrace resolves the argument to a trace directory: a directory
// itself, a session id or prefix in this directory, or, with none, the
// newest session here that has a trace.
func findTrace(arg string) (dir, title string, err error) {
	if arg != "" {
		if fi, err := os.Stat(filepath.Join(arg, trace.EventsFile)); err == nil && !fi.IsDir() {
			return arg, filepath.Base(arg), nil
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return "", "", err
	}
	sessDir := session.Dir(cfg.DataDir, cwd)
	if arg != "" {
		path, err := session.Find(sessDir, arg)
		if err != nil {
			return "", "", err
		}
		dir = session.TraceDir(path)
		if _, err := os.Stat(filepath.Join(dir, trace.EventsFile)); err != nil {
			return "", "", fmt.Errorf("session %s has no trace; record one with larik --debug, LARIK_DEBUG=1 or /debug on", arg)
		}
		return dir, sessionID(path), nil
	}
	infos, err := session.List(sessDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	for _, in := range infos {
		dir := session.TraceDir(in.Path)
		if _, err := os.Stat(filepath.Join(dir, trace.EventsFile)); err == nil {
			return dir, in.ID, nil
		}
	}
	return "", "", errors.New("no traced session in this directory; record one with larik --debug, LARIK_DEBUG=1 or /debug on")
}

func sessionID(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// envDebug reports whether LARIK_DEBUG asks for debug mode.
func envDebug() bool {
	switch os.Getenv("LARIK_DEBUG") {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

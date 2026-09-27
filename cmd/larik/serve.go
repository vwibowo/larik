package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"larik/internal/app"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/server"
)

// runServe implements `larik serve`: the HTTP + SSE API.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var (
		addr        = fs.String("addr", "127.0.0.1:4096", "listen address (port 0 picks a free port)")
		token       = fs.String("token", "", "bearer token clients must send (default: $LARIK_SERVER_TOKEN, else random)")
		model       = fs.String("model", "", "default provider/model for new sessions")
		effort      = fs.String("effort", "", "default reasoning effort for new sessions")
		mode        = fs.String("mode", "", "default permission mode for new sessions")
		allowRemote = fs.Bool("allow-remote", false, "allow listening on non-loopback addresses (anyone who can reach the port and has the token can run commands)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: larik serve [flags]\n\nServe the Larik HTTP + SSE API for the current directory.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *token == "" {
		*token = os.Getenv("LARIK_SERVER_TOKEN")
	}
	if *token == "" {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		*token = hex.EncodeToString(b)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsLoopback() && !*allowRemote {
		ln.Close()
		return errors.New("refusing to listen on a non-loopback address without --allow-remote")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Fail fast on bad defaults instead of on the first session.
	if _, err := app.ParseEffort(*effort); err != nil {
		return err
	}
	if *mode != "" {
		if _, err := permission.ParseMode(*mode); err != nil {
			return err
		}
	}
	a, err := app.Setup(cwd, version)
	if err != nil {
		return err
	}
	defer a.Close()
	if *model != "" {
		if _, err := providers.Resolve(a.Cfg, *model); err != nil {
			return err
		}
	}
	srv := server.New(a, server.Options{
		Token:        *token,
		Defaults:     app.Options{Model: *model, Effort: *effort, Mode: *mode},
		AllowAnyHost: *allowRemote,
	})

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + ln.Addr().String()
	// One JSON line on stdout for programs that spawn the server.
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": url, "token": *token})
	fmt.Fprintf(os.Stderr, "larik serving %s on %s\n", cwd, url)
	if a.SandboxNote != "" {
		fmt.Fprintln(os.Stderr, "! "+a.SandboxNote)
	}
	if a.SearchNote != "" {
		fmt.Fprintln(os.Stderr, "! web_search disabled: "+a.SearchNote)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		srv.Close()
		return err
	case <-ctx.Done():
	}
	fmt.Fprintln(os.Stderr, "shutting down")
	// Ending the sessions closes their event streams, so Shutdown can
	// finish instead of waiting on open SSE connections.
	srv.Close()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}

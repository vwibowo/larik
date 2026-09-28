// Command website builds Larik's static website from the repository's
// README.md and docs/, plus the templates and content in this directory.
//
//	go run .                 # build into dist/
//	go run . -serve :8080    # build, serve, and rebuild on every page load
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	out := flag.String("out", "dist", "output directory (deleted and recreated)")
	root := flag.String("root", "..", "repository root holding README.md and docs/")
	base := flag.String("base", "", "URL path prefix when the site isn't served at the domain root, e.g. /larik")
	serve := flag.String("serve", "", "after building, serve the site on this address and rebuild on each page load")
	flag.Parse()

	abs, err := filepath.Abs(*out)
	if err != nil || abs == "/" || abs == mustAbs(*root) || abs == mustAbs(".") {
		log.Fatalf("refusing to use %q as the output directory", *out)
	}
	run := func() error {
		start := time.Now()
		if err := build(*root, ".", *out, *base); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "built %s in %s\n", *out, time.Since(start).Round(time.Millisecond))
		return nil
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
	if *serve == "" {
		return
	}

	var mu sync.Mutex
	files := http.FileServer(http.Dir(*out))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || strings.HasSuffix(r.URL.Path, ".html") {
			mu.Lock()
			err := run()
			mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if _, err := os.Stat(filepath.Join(*out, filepath.FromSlash(r.URL.Path))); os.IsNotExist(err) {
			page, _ := os.ReadFile(filepath.Join(*out, "404.html"))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(page)
			return
		}
		files.ServeHTTP(w, r)
	})
	fmt.Fprintf(os.Stderr, "serving on http://%s\n", strings.Replace(*serve, ":", "localhost:", 1))
	log.Fatal(http.ListenAndServe(*serve, nil))
}

func mustAbs(p string) string {
	a, _ := filepath.Abs(p)
	return a
}

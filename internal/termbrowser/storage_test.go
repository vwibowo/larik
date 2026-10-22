package termbrowser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Cookies and storage outlive Chrome: /browser quit or quitting Larik
// must close Chrome cleanly, or it loses what it hadn't written out yet.
func TestStorageSurvivesARestart(t *testing.T) {
	path := chrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "srv", Value: "1", MaxAge: 3600, Path: "/"})
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<title>s</title>"))
	}))
	defer srv.Close()
	profile, _ := os.MkdirTemp("", "larik-browser-test")
	defer os.RemoveAll(profile)
	ctx := context.Background()
	open := func() *Session {
		s := New(Options{ChromePath: path, ProfileDir: profile})
		s.startupTimeout = testStartupTimeout
		return s
	}
	s := open()
	if _, err := s.Navigate(ctx, srv.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	out, err := s.Eval(ctx, `(async () => {
		document.cookie = "js=2; max-age=3600; path=/";
		localStorage.setItem("ls", "3");
		sessionStorage.setItem("ss", "4");
		await new Promise((ok, bad) => { const r = indexedDB.open("db", 1);
			r.onupgradeneeded = () => r.result.createObjectStore("s");
			r.onsuccess = () => { const tx = r.result.transaction("s", "readwrite"); tx.objectStore("s").put("5", "k"); tx.oncomplete = ok; tx.onerror = bad; };
			r.onerror = bad; });
		return [document.cookie, localStorage.getItem("ls"), sessionStorage.getItem("ss"), navigator.cookieEnabled];
	})()`)
	if err != nil || out != `["srv=1; js=2","3","4",true]` {
		t.Fatalf("first run: %s %v", out, err)
	}
	s.Close() // as the pane closing or Larik quitting does
	s = open()
	defer s.Close()
	if _, err := s.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	out, err = s.Eval(ctx, `(async () => {
		const idb = await new Promise((ok) => { const r = indexedDB.open("db", 1);
			r.onupgradeneeded = () => r.result.createObjectStore("s");
			r.onsuccess = () => { const g = r.result.transaction("s").objectStore("s").get("k"); g.onsuccess = () => ok(g.result ?? null); g.onerror = () => ok("err"); };
			r.onerror = () => ok("open-err"); });
		return {cookie: document.cookie, local: localStorage.getItem("ls"), session: sessionStorage.getItem("ss"), idb};
	})()`)
	// sessionStorage, like a real browser's, ends with the session.
	if err != nil || out != `{"cookie":"srv=1; js=2","local":"3","session":null,"idb":"5"}` {
		t.Fatalf("after restart: %s %v", out, err)
	}
}

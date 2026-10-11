package browserbroker

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHome points the broker at a temporary home and cache, so the path rules
// can be tested without touching the real ones.
func fakeHome(t *testing.T) (home, cache string) {
	t.Helper()
	home, cache = filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "larik"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "Larik"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	oldHome, oldCache := userHomeDir, userCacheDir
	userHomeDir = func() (string, error) { return home, nil }
	userCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userHomeDir, userCacheDir = oldHome, oldCache })
	return home, cache
}

// fakeBundle makes a bundle that looks like Chrome, with a stubbed signature
// check: the real one needs an installed, Google-signed Chrome.
func fakeBundle(t *testing.T, dir, name, executable string) string {
	t.Helper()
	app := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", executable), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestLaunchAgentPlist(t *testing.T) {
	exe := "/Applications/Larik & <test>/larik"
	log := "/Users/x/Library/Logs/Larik/browser-broker.log"
	p := launchAgentPlist(exe, log)
	var parsed struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(p), &parsed); err != nil {
		t.Fatalf("invalid plist XML: %v", err)
	}
	for _, want := range []string{"<key>Label</key><string>" + label + "</string>", "<string>/Applications/Larik &amp; &lt;test&gt;/larik</string>", "<string>browser-broker</string><string>run</string>", "<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>", "<key>StandardErrorPath</key><string>" + log + "</string>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q: %s", want, p)
		}
	}
	// The log must be a real path: a broker that cannot start is otherwise
	// invisible, since launchd restarts it forever.
	if strings.Contains(p, "/dev/null") {
		t.Error("the LaunchAgent must not discard its output")
	}
}

func TestBrokerRequestProtocolJSON(t *testing.T) {
	want := request{Op: "launch", AppPath: "/Applications/Google Chrome.app", Headless: true}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got request
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("JSON request roundtrip: got %+v want %+v", got, want)
	}
	original := response{WebSocketURL: "ws://127.0.0.1:43123/devtools/browser/test", Profile: "/Users/test/Library/Caches/Larik/browser-profile-x"}
	data, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded response
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("JSON response roundtrip: got %+v want %+v", decoded, original)
	}
}

// The deadlines have to grow outward, or the side that gives up first leaves a
// browser running that nobody knows the profile of.
func TestDeadlinesGrowOutward(t *testing.T) {
	if !(launchWait < serverDeadline && serverDeadline < clientDeadline) {
		t.Fatalf("launchWait=%s serverDeadline=%s clientDeadline=%s", launchWait, serverDeadline, clientDeadline)
	}
}

func TestValidateApp(t *testing.T) {
	fakeHome(t)
	old := verifySignature
	var verified string
	verifySignature = func(app string) error { verified = app; return nil }
	t.Cleanup(func() { verifySignature = old })

	// A real Chrome is the only positive case the rules allow, since the
	// bundle must be in /Applications.
	chrome := filepath.Join(appRoot, "Google Chrome.app")
	if _, err := os.Stat(filepath.Join(chrome, "Contents", "MacOS", "Google Chrome")); err == nil {
		got, err := validateApp(chrome)
		if err != nil || got == "" {
			t.Fatalf("validateApp(%q) = %q, %v", chrome, got, err)
		}
		if verified == "" {
			t.Error("the signature was not checked")
		}
	} else {
		t.Log("no Google Chrome installed; only the rejections are checked")
	}

	outside := fakeBundle(t, t.TempDir(), "Google Chrome.app", "Google Chrome")
	for _, tc := range []struct{ name, app string }{
		{"not an app bundle", "/Applications/Google Chrome"},
		{"relative", "Google Chrome.app"},
		{"unclean", "/Applications/../Applications/Google Chrome.app"},
		{"not chrome", "/Applications/Safari.app"},
		{"outside /Applications", outside},
		{"home Applications", filepath.Join(os.Getenv("HOME"), "Applications", "Google Chrome.app")},
		{"missing", "/Applications/Larik No Such Chrome.app"},
	} {
		if got, err := validateApp(tc.app); err == nil {
			t.Errorf("%s: accepted %q as %q", tc.name, tc.app, got)
		}
	}
}

// The signature check is the whole point of the broker: it runs outside every
// sandbox, so a bundle anyone could write must not be started.
func TestValidateAppRequiresSignature(t *testing.T) {
	fakeHome(t)
	chrome := filepath.Join(appRoot, "Google Chrome.app")
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("no Google Chrome installed")
	}
	old := verifySignature
	verifySignature = func(string) error { return errors.New("unsigned") }
	t.Cleanup(func() { verifySignature = old })
	if _, err := validateApp(chrome); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("an unsigned bundle must be refused, got %v", err)
	}
}

func TestCodesignVerify(t *testing.T) {
	chrome := filepath.Join(appRoot, "Google Chrome.app")
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("no Google Chrome installed")
	}
	if err := codesignVerify(chrome); err != nil {
		t.Errorf("an installed Google Chrome should verify: %v", err)
	}
	// Any other signed app fails the Google requirement.
	for _, other := range []string{"/System/Applications/Calculator.app", "/Applications/Safari.app", "/System/Library/CoreServices/Finder.app"} {
		if _, err := os.Stat(other); err != nil {
			continue
		}
		if err := codesignVerify(other); err == nil {
			t.Errorf("%s satisfied the Google Chrome requirement", other)
		}
		return
	}
}

func TestValidateProfile(t *testing.T) {
	home, cache := fakeHome(t)
	data := filepath.Join(home, ".local", "share", "larik")
	temp := filepath.Join(cache, "Larik", tempProfilePrefix+"abc")
	for _, ok := range []string{filepath.Join(data, "browser-profile"), filepath.Join(data, "browser-profile", "sub"), temp} {
		if err := validateProfile(ok); err != nil {
			t.Errorf("validateProfile(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "relative", "/etc", "/tmp/../etc", filepath.Join(home, ".ssh"), filepath.Join(home, ".local", "share"),
		filepath.Join(cache, "Larik"),                               // the root itself is not a profile
		filepath.Join(cache, "Other", tempProfilePrefix+"abc"),      // wrong parent
		filepath.Join(cache, "Larik", "sub", tempProfilePrefix+"x"), // not directly in the root
		filepath.Join(cache, "Larik", "something-else"),
	} {
		if err := validateProfile(bad); err == nil {
			t.Errorf("validateProfile(%q) accepted a profile outside the allowed roots", bad)
		}
	}
	// Only a temporary profile may be removed, never the real one.
	if err := validateTemporaryProfile(temp); err != nil {
		t.Errorf("validateTemporaryProfile(%q) = %v", temp, err)
	}
	if err := validateTemporaryProfile(filepath.Join(data, "browser-profile")); err == nil {
		t.Error("the persistent profile must not be removable through the broker")
	}
}

// XDG_DATA_HOME moves the data directory, and a symlinked path must still be
// recognized once Chrome and the broker resolve it.
func TestValidateProfileFollowsDataDir(t *testing.T) {
	home, _ := fakeHome(t)
	real := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(filepath.Join(real, "larik"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "linked-data")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", link)
	for _, ok := range []string{filepath.Join(link, "larik", "browser-profile"), filepath.Join(real, "larik", "browser-profile")} {
		if err := validateProfile(ok); err != nil {
			t.Errorf("validateProfile(%q) = %v, want nil for a configured data directory", ok, err)
		}
	}
	if err := validateProfile(filepath.Join(real, "other", "profile")); err == nil {
		t.Error("a sibling of the data directory must not be accepted")
	}
}

// ask runs one request against handle over a socket pair.
func ask(t *testing.T, q any) response {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); handle(server) }()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(client).Encode(q); err != nil {
		t.Fatal(err)
	}
	var r response
	if err := json.NewDecoder(client).Decode(&r); err != nil {
		t.Fatalf("decoding the broker reply: %v", err)
	}
	client.Close()
	<-done
	return r
}

func TestHandleRemove(t *testing.T) {
	_, cache := fakeHome(t)
	root := filepath.Join(cache, "Larik")
	profile := filepath.Join(root, tempProfilePrefix+"gone")
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if r := ask(t, request{Op: "remove", Profile: profile}); r.Error != "" {
		t.Fatalf("remove: %s", r.Error)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("the temporary profile was not removed: %v", err)
	}
	// Anything else under the cache stays.
	keep := filepath.Join(root, "keep-me")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := ask(t, request{Op: "remove", Profile: keep}); r.Error == "" {
		t.Error("the broker removed a directory that is not one of its profiles")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("an unrelated directory was removed: %v", err)
	}
}

func TestHandleRejectsBadRequests(t *testing.T) {
	fakeHome(t)
	for _, tc := range []struct {
		name string
		q    any
	}{
		{"unsupported op", request{Op: "exec"}},
		{"unknown field", map[string]any{"op": "launch", "sudo": true}},
		{"untrusted app", request{Op: "launch", AppPath: "/Applications/Safari.app"}},
		{"no app", request{Op: "launch"}},
	} {
		if r := ask(t, tc.q); r.Error == "" {
			t.Errorf("%s: accepted %+v", tc.name, tc.q)
		}
	}
}

// A refused request must not leave a profile directory behind.
func TestHandleLeavesNoProfileWhenRefused(t *testing.T) {
	_, cache := fakeHome(t)
	root := filepath.Join(cache, "Larik")
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if r := ask(t, request{Op: "launch", AppPath: "/Applications/Safari.app"}); r.Error == "" {
		t.Fatal("Safari was accepted")
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused launch left %d entries behind in %s", len(after)-len(before), root)
	}
}

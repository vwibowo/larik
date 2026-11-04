package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemoteMetadataUsesHostRepository(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	_, connect := standIn(t, data)
	dial, _, stop, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	meta, err := readRemoteMeta(remoteMetaSource{name: "devbox", dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	if got := meta.Git[repo]; got.Branch == "" {
		t.Fatalf("remote repository metadata: %+v", meta.Git)
	}
}

func TestRemotePortForwardArgumentsAndSavedIdentity(t *testing.T) {
	args := strings.Join(sshPortForwardArgs("devbox", 43210, 3000), " ")
	if !strings.Contains(args, "-N -T") || !strings.Contains(args, "ExitOnForwardFailure=yes") || !strings.Contains(args, "-L 127.0.0.1:43210:127.0.0.1:3000 devbox") {
		t.Fatalf("SSH forward: %q", args)
	}
	a := New(Options{DataDir: t.TempDir()})
	defer a.Close()
	b := a.startBrowser("http://127.0.0.1:43210")
	b.dir, b.remotePort = remotePath("devbox", ""), remotePortKey{"devbox", 3000}
	st := a.state()
	if len(st.Tabs) != 1 || st.Browsers[0] != blankPage || st.RemotePorts[0] != (savedRemotePort{"devbox", 3000}) {
		t.Fatalf("saved remote browser: %+v", st)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var restored savedState
	if err := json.Unmarshal(data, &restored); err != nil || restored.RemotePorts[0] != st.RemotePorts[0] {
		t.Fatalf("restored remote port: %+v, %v", restored.RemotePorts, err)
	}
}

func TestRemoteForwardStopsWhenPortDisappears(t *testing.T) {
	a := New(Options{DataDir: t.TempDir()})
	defer a.Close()
	key := remotePortKey{"devbox", 3000}
	b := &tab{id: "browser", browser: true, remotePort: key}
	a.tabs = []*tab{b}
	stopped := 0
	a.forwards = map[remotePortKey]*remoteForward{key: {local: 43210, stop: func() { stopped++ }}}
	a.expireRemoteForwards(map[string][]int{remotePath("devbox", "/session"): {3000}}, map[string]bool{"devbox": true})
	if stopped != 0 || b.remotePortLost {
		t.Fatal("live port forward was stopped")
	}
	a.expireRemoteForwards(map[string][]int{}, map[string]bool{"devbox": true})
	if stopped != 1 || !b.remotePortLost || len(a.forwards) != 0 {
		t.Fatalf("stale forward: stopped=%d lost=%v", stopped, b.remotePortLost)
	}
}

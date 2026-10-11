package browserbroker

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
)

func TestLaunchAgentPlist(t *testing.T) {
	exe := "/Applications/Larik & <test>/larik"
	p := launchAgentPlist(exe)
	var parsed struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(p), &parsed); err != nil {
		t.Fatalf("invalid plist XML: %v", err)
	}
	for _, want := range []string{"<key>Label</key><string>" + label + "</string>", "<string>/Applications/Larik &amp; &lt;test&gt;/larik</string>", "<string>browser-broker</string><string>run</string>", "<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q: %s", want, p)
		}
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
func TestValidateRejectsUntrustedTargets(t *testing.T) {
	for _, q := range []request{{AppPath: "/Applications/Safari.app", Profile: "/tmp/larik-browser-test"}, {AppPath: "/Applications/Google Chrome.app", Profile: "/etc"}, {AppPath: "/Applications/Google Chrome.app", Profile: "relative"}, {AppPath: "/Applications/Google Chrome.app", Profile: "/tmp/../etc"}} {
		if validate(q) == nil {
			t.Errorf("accepted unsafe request: %+v", q)
		}
	}
}

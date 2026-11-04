package desktop

import (
	"strings"
	"testing"
)

func TestPageWaitAndActionsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{State: "load"}, "readyState"},
		{Request{State: "url", URL: "http://localhost:3000/route"}, "location.href"},
		{Request{State: "text", Text: "Loaded"}, "innerText.includes"},
		{Request{State: "visible", Ref: "e12"}, "getClientRects"},
		{Request{State: "enabled", Ref: "e12"}, "!el.disabled"},
	} {
		script, err := pageWaitScript(tc.req)
		if err != nil || !strings.Contains(script, tc.want) {
			t.Fatalf("%s: %q, %v", tc.req.State, script, err)
		}
	}
	if _, err := pageWaitScript(Request{State: "url"}); err == nil {
		t.Fatal("URL wait without target accepted")
	}
	if _, err := pageWaitScript(Request{State: "visible"}); err == nil {
		t.Fatal("element wait without ref accepted")
	}
	if _, err := pagePressScript("", "Enter\nrm"); err == nil {
		t.Fatal("invalid key accepted")
	}
	if script, err := pagePressScript("e12", "Enter"); err != nil || !strings.Contains(script, "requestSubmit") {
		t.Fatalf("press: %q, %v", script, err)
	}
	if script := pageSelectScript("e12", "one\";bad"); !strings.Contains(script, `one\";bad`) || strings.Contains(script, "el.value = one") {
		t.Fatalf("select value was not quoted: %q", script)
	}
}

package desktoppage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"larik/internal/desktop"
	"larik/internal/termbrowser"
	"larik/internal/tools"
)

// fake answers page commands as a window would, keeping what it was asked.
type fake struct {
	asked []desktop.Request
	res   desktop.Response
	err   error
}

func (f *fake) call(req desktop.Request) (desktop.Response, error) {
	f.asked = append(f.asked, req)
	return f.res, f.err
}

func run(t *testing.T, tool tools.Tool, input string) tools.Result {
	t.Helper()
	return tool.Run(context.Background(), &tools.Env{}, json.RawMessage(input))
}

func byName(call Caller, name string) tools.Tool {
	for _, t := range ToolsWith(call) {
		if t.Spec().Name == name {
			return t
		}
	}
	return nil
}

func TestNavigateReturnsTheOutline(t *testing.T) {
	long := strings.Repeat("- text \"line\"\n", 3000)
	f := &fake{res: desktop.Response{Page: &desktop.PageResult{Snapshot: &termbrowser.Snapshot{URL: "http://localhost:3000/", Title: "App", Text: "- button \"Save\" [ref=e1]\n" + long, Refs: 1}}}}
	res := run(t, byName(f.call, "desktop_page_navigate"), `{"url":"localhost:3000"}`)
	if res.IsError || len(f.asked) != 1 || f.asked[0].Cmd != "page" || f.asked[0].Op != desktop.PageNavigate || f.asked[0].URL != "localhost:3000" {
		t.Fatalf("asked %+v: %s", f.asked, res.Content)
	}
	for _, want := range []string{"Title: App", `button "Save" [ref=e1]`, "<web_content", "desktop_page_snapshot with start="} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("no %q in %s", want, res.Content[:300])
		}
	}
	if strings.Contains(res.Content, "browser_snapshot") {
		t.Fatal("the hint names the headless browser's tool")
	}
}

func TestActions(t *testing.T) {
	f := &fake{res: desktop.Response{Page: &desktop.PageResult{Snapshot: &termbrowser.Snapshot{URL: "u", Text: "- text \"done\""}}}}
	if res := run(t, byName(f.call, "desktop_page_type"), `{"ref":"e2","text":"Ada","submit":true}`); res.IsError || !strings.HasPrefix(res.Content, "Typed into e2 and submitted it.") {
		t.Fatal(res.Content)
	}
	if r := f.asked[0]; r.Op != desktop.PageType || r.Ref != "e2" || r.Text != "Ada" || !r.Enter {
		t.Fatalf("type asked %+v", r)
	}
	run(t, byName(f.call, "desktop_page_click"), `{"ref":"e4"}`)
	run(t, byName(f.call, "desktop_page_history"), `{"action":"back"}`)
	run(t, byName(f.call, "desktop_page_snapshot"), `{}`)
	var ops []string
	for _, r := range f.asked[1:] {
		ops = append(ops, r.Op+":"+r.Ref)
	}
	if strings.Join(ops, " ") != "click:e4 back: snapshot:" {
		t.Fatalf("ops %v", ops)
	}
	if res := run(t, byName(f.call, "desktop_page_history"), `{"action":"sideways"}`); !res.IsError {
		t.Fatal("an unknown action")
	}
	f.err = errors.New("no browser tab is open")
	if res := run(t, byName(f.call, "desktop_page_click"), `{"ref":"e1"}`); !res.IsError || !strings.Contains(res.Content, "no browser tab") {
		t.Fatal(res.Content)
	}
}

func TestEvalAndScreenshot(t *testing.T) {
	f := &fake{res: desktop.Response{Page: &desktop.PageResult{Value: `{"n":3}`}}}
	if res := run(t, byName(f.call, "desktop_page_eval"), `{"code":"({n: 3})"}`); !strings.Contains(res.Content, `{"n":3}`) || f.asked[0].Text != "({n: 3})" {
		t.Fatal(res.Content)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3000, 2000)))
	f.res = desktop.Response{Tab: &desktop.TabInfo{URL: "http://localhost:3000/"}, Page: &desktop.PageResult{PNG: buf.Bytes()}}
	res := run(t, byName(f.call, "desktop_page_screenshot"), `{}`)
	if res.IsError || len(res.Images) != 1 || !strings.HasPrefix(res.Images[0].MediaType, "image/") || !strings.Contains(res.Content, "localhost:3000") {
		t.Fatalf("screenshot: %s, %d images", res.Content, len(res.Images))
	}
}

func TestReadOnly(t *testing.T) {
	for _, tool := range ToolsWith(nil) {
		name := tool.Spec().Name
		if want := name == "desktop_page_snapshot" || name == "desktop_page_screenshot" || name == "desktop_page_wait" || name == "desktop_page_console"; tool.ReadOnly() != want {
			t.Errorf("%s read-only %v", name, tool.ReadOnly())
		}
		var schema map[string]any
		if json.Unmarshal(tool.Spec().Schema, &schema) != nil {
			t.Errorf("%s schema", name)
		}
	}
}

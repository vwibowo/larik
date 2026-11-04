package desktopterminal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/desktop"
	"larik/internal/tools"
)

func TestTerminalToolsRouteToExactTerminalTab(t *testing.T) {
	var calls []desktop.Request
	call := func(req desktop.Request) (desktop.Response, error) {
		calls = append(calls, req)
		switch req.Cmd {
		case "list":
			return desktop.Response{Tabs: []desktop.TabInfo{{ID: "term-1", Kind: "terminal"}, {ID: "browser-1", Kind: "browser"}}}, nil
		case "open":
			return desktop.Response{Tab: &desktop.TabInfo{ID: "term-2", Kind: "terminal"}}, nil
		case "read":
			return desktop.Response{Text: "terminal output"}, nil
		default:
			return desktop.Response{}, nil
		}
	}
	var list List = List{call}
	var open Open = Open{call}
	var read Read = Read{call}
	var send Send = Send{call}
	if !list.ReadOnly() || !read.ReadOnly() || open.ReadOnly() || send.ReadOnly() {
		t.Fatal("terminal tool permission classifications are wrong")
	}
	if got := list.Run(context.Background(), nil, nil); got.IsError || !strings.Contains(got.Content, `"term-1"`) {
		t.Fatalf("list: %+v", got)
	}
	env := tools.NewEnv(t.TempDir())
	if got := open.Run(context.Background(), env, json.RawMessage(`{"directory":"child","placement":"below"}`)); got.IsError || !strings.Contains(got.Content, "term-2") {
		t.Fatalf("open: %+v", got)
	}
	opened := calls[len(calls)-1]
	if !opened.Shell || !opened.Below || opened.Split || !strings.HasSuffix(opened.Cwd, "/child") {
		t.Fatalf("terminal placement: %+v", opened)
	}
	if got := read.Run(context.Background(), nil, json.RawMessage(`{"tab_id":"term-1"}`)); got.IsError || got.Content != "terminal output" {
		t.Fatalf("read: %+v", got)
	}
	if got := send.Run(context.Background(), nil, json.RawMessage(`{"tab_id":"term-1","text":"pwd"}`)); got.IsError {
		t.Fatalf("send: %+v", got)
	}
	sent := calls[len(calls)-1]
	if sent.Cmd != "send" || sent.Tab != "term-1" || sent.Text != "pwd" || !sent.Enter {
		t.Fatalf("send request: %+v", sent)
	}
	before := len(calls)
	if got := send.Run(context.Background(), nil, json.RawMessage(`{"tab_id":"browser-1","text":"pwd"}`)); !got.IsError || !strings.Contains(got.Content, "not a terminal") || len(calls) != before+1 {
		t.Fatalf("browser tab accepted terminal input: %+v %v", got, calls[before:])
	}
	if got := read.Run(context.Background(), nil, json.RawMessage(`{"tab_id":"term"}`)); !got.IsError || !strings.Contains(got.Content, "exact ID") {
		t.Fatalf("prefix accepted as a terminal ID: %+v", got)
	}
}

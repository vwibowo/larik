// Package desktopterminal gives Larik sessions inside Desktop direct tools
// for terminal tabs, without routing control through a sandboxed shell.
package desktopterminal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"larik/internal/desktop"
	"larik/internal/llm"
	"larik/internal/tools"
)

type Caller func(desktop.Request) (desktop.Response, error)

func Tools(dataDir string) []tools.Tool {
	return ToolsWith(func(req desktop.Request) (desktop.Response, error) {
		return desktop.Call(dataDir, req)
	})
}

func ToolsWith(call Caller) []tools.Tool {
	return []tools.Tool{List{call}, Open{call}, Read{call}, Send{call}}
}

func spec(name, description, schema string) llm.ToolSpec {
	return llm.ToolSpec{Name: name, Description: description, Schema: json.RawMessage(schema)}
}

func failure(err error) tools.Result {
	return tools.Result{Content: "Error: " + err.Error(), IsError: true}
}

func terminalTab(call Caller, id string) error {
	res, err := call(desktop.Request{Cmd: "list"})
	if err != nil {
		return err
	}
	for _, tab := range res.Tabs {
		if tab.ID == id {
			if tab.Kind != "terminal" {
				return fmt.Errorf("tab %s is %s, not a terminal", id, tab.Kind)
			}
			return nil
		}
	}
	return fmt.Errorf("terminal tab %s was not found; use desktop_tabs_list for its exact ID", id)
}

// List is desktop_tabs_list. It includes exact IDs for terminal and browser tabs.
type List struct{ Call Caller }

func (List) ReadOnly() bool { return true }
func (List) Spec() llm.ToolSpec {
	return spec("desktop_tabs_list", "List Larik Desktop tabs with their exact IDs, kinds, titles and states. Use a returned ID for terminal read and send; this reads the visible Desktop window or its hosted tabs.", `{"type":"object","properties":{}}`)
}
func (t List) Run(_ context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
	res, err := t.Call(desktop.Request{Cmd: "list"})
	if err != nil {
		return failure(err)
	}
	data, err := json.MarshalIndent(res.Tabs, "", "  ")
	if err != nil {
		return failure(err)
	}
	return tools.Result{Content: string(data)}
}

// Open is desktop_terminal_open. It starts a shell in a new Desktop tab.
type Open struct{ Call Caller }

func (Open) ReadOnly() bool { return false }
func (Open) Spec() llm.ToolSpec {
	return spec("desktop_terminal_open", "Open a shell terminal in Larik Desktop, beside the focused pane by default. This starts a terminal outside Larik's bash sandbox and needs approval. Returns the new tab ID.", `{"type":"object","properties":{"directory":{"type":"string","description":"Directory for the shell; defaults to this session's working directory"},"placement":{"type":"string","enum":["right","below","tab"],"description":"right (default) splits beside the focused pane; below splits underneath; tab uses the focused pane"}}}`)
}
func (t Open) Run(_ context.Context, env *tools.Env, input json.RawMessage) tools.Result {
	var in struct{ Directory, Placement string }
	if len(input) > 0 && json.Unmarshal(input, &in) != nil {
		return failure(fmt.Errorf("expected directory and placement"))
	}
	if env == nil || env.Cwd == "" {
		return failure(fmt.Errorf("working directory is unavailable"))
	}
	dir := in.Directory
	if dir == "" {
		dir = env.Cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(env.Cwd, dir)
	}
	req := desktop.Request{Cmd: "open", Cwd: filepath.Clean(dir), Shell: true}
	switch in.Placement {
	case "", "right":
		req.Split = true
	case "below":
		req.Below = true
	case "tab":
	default:
		return failure(fmt.Errorf("placement must be right, below or tab"))
	}
	res, err := t.Call(req)
	if err != nil {
		return failure(err)
	}
	if res.Tab == nil {
		return failure(fmt.Errorf("Desktop did not return the terminal tab"))
	}
	return tools.Result{Content: "Opened terminal tab " + res.Tab.ID + " in " + req.Cwd, Data: res.Tab}
}

// Read is desktop_terminal_read. Desktop rejects browser and Files tabs.
type Read struct{ Call Caller }

func (Read) ReadOnly() bool { return true }
func (Read) Spec() llm.ToolSpec {
	return spec("desktop_terminal_read", "Read a Larik Desktop terminal tab's latest screen and scrollback. Give its exact tab ID from desktop_tabs_list.", `{"type":"object","properties":{"tab_id":{"type":"string"},"lines":{"type":"integer","minimum":1,"maximum":200,"description":"Recent lines to read, default 50"}},"required":["tab_id"]}`)
}
func (t Read) Run(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		TabID string `json:"tab_id"`
		Lines int    `json:"lines"`
	}
	if json.Unmarshal(input, &in) != nil || in.TabID == "" || in.Lines < 0 || in.Lines > 200 {
		return failure(fmt.Errorf("expected tab_id and lines from 1 to 200"))
	}
	if in.Lines == 0 {
		in.Lines = 50
	}
	if err := terminalTab(t.Call, in.TabID); err != nil {
		return failure(err)
	}
	res, err := t.Call(desktop.Request{Cmd: "read", Tab: in.TabID, Lines: in.Lines})
	if err != nil {
		return failure(err)
	}
	return tools.Result{Content: res.Text}
}

// Send is desktop_terminal_send. Sending with Enter runs the text in the tab.
type Send struct{ Call Caller }

func (Send) ReadOnly() bool { return false }
func (Send) Spec() llm.ToolSpec {
	return spec("desktop_terminal_send", "Type text into a Larik Desktop terminal tab by exact ID. By default, press Enter afterward, which can execute a command in that terminal outside Larik's bash sandbox; user approval is required. Set enter=false to type without pressing Enter.", `{"type":"object","properties":{"tab_id":{"type":"string"},"text":{"type":"string"},"enter":{"type":"boolean","description":"Press Enter after typing; default true"}},"required":["tab_id","text"]}`)
}
func (t Send) Run(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		TabID string `json:"tab_id"`
		Text  string `json:"text"`
		Enter *bool  `json:"enter"`
	}
	if json.Unmarshal(input, &in) != nil || in.TabID == "" || in.Text == "" {
		return failure(fmt.Errorf("expected tab_id and nonempty text"))
	}
	enter := in.Enter == nil || *in.Enter
	if err := terminalTab(t.Call, in.TabID); err != nil {
		return failure(err)
	}
	_, err := t.Call(desktop.Request{Cmd: "send", Tab: in.TabID, Text: in.Text, Enter: enter})
	if err != nil {
		return failure(err)
	}
	return tools.Result{Content: "Sent text to terminal tab " + in.TabID}
}

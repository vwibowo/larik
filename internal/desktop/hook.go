package desktop

import "encoding/json"

// HookRequest translates a coding agent's lifecycle event into a terminal
// status. It deliberately reads only event metadata; prompts and tool inputs
// never enter the report or Desktop's notifications.
func HookRequest(agent, event, tab string, input []byte) (Request, bool) {
	if tab == "" {
		return Request{}, false
	}
	var meta struct {
		NotificationType string `json:"notification_type"`
		Type             string `json:"type"`
		Properties       struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"properties"`
	}
	if !json.Valid(input) || json.Unmarshal(input, &meta) != nil {
		return Request{}, false
	}
	var state, kind, label, message string
	switch agent {
	case "claude":
		label = "Claude Code"
		switch event {
		case "UserPromptSubmit", "PostToolUse":
			state = "working"
		case "PermissionRequest":
			state, kind = "blocked", "permission"
		case "Notification":
			if meta.NotificationType == "permission_prompt" {
				state, kind = "blocked", "permission"
			}
		case "Stop":
			state = "done"
		case "StopFailure":
			state = "error"
		}
	case "codex":
		label = "Codex"
		switch event {
		case "UserPromptSubmit", "PostToolUse":
			state = "working"
		case "PermissionRequest":
			state, kind = "blocked", "permission"
		case "Stop":
			state = "done"
		case "Interrupt":
			state = "idle"
		}
	case "opencode":
		label = "OpenCode"
		switch event {
		case "session.status":
			if meta.Properties.Status.Type == "busy" || meta.Properties.Status.Type == "retry" {
				state = "working"
			}
		case "permission.asked":
			state, kind = "blocked", "permission"
		case "permission.replied":
			state = "working"
		case "session.idle":
			state = "done"
		case "session.error":
			state = "error"
		}
	case "gemini":
		label = "Gemini CLI"
		switch event {
		case "BeforeAgent", "AfterTool":
			state = "working"
		case "AfterAgent":
			state = "done"
		case "Notification":
			if meta.NotificationType == "ToolPermission" {
				state, kind = "blocked", "permission"
			}
		}
	default:
		return Request{}, false
	}
	if state == "" {
		return Request{}, false
	}
	switch state {
	case "blocked":
		message = "Needs permission"
	case "done":
		message = "Finished"
	case "error":
		message = "Failed"
	case "working":
		message = "Working"
	}
	return Request{Cmd: "status", Tab: tab, StatusID: agent, State: state, Kind: kind, Title: label, Message: message}, true
}

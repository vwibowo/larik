package tui

import "math/rand/v2"

// tips rotate under the spinner while a turn runs (spinner_tips setting).
var tips = []string{
	"shift+tab cycles the permission mode; /mode shows them all",
	"alt+p switches model and effort without leaving the prompt",
	"ctrl+o shows the model's thinking in full",
	"enter during a turn queues a follow-up for when it ends",
	"/rewind branches off an earlier prompt so you can redo it",
	"/fork continues this conversation in a new session",
	"/undo reverts the file changes from the last turn",
	"/compact frees context by summarizing the conversation",
	"/config changes theme, notifications, language and more",
	"/theme light or /theme dark overrides the terminal's background",
	"add AGENTS.md to a project to give larik standing instructions",
	"? on an empty prompt lists every shortcut",
}

func nextTip() string { return tips[rand.IntN(len(tips))] }

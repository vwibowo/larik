package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Prompt hooks ask a model to judge the hook input, as in Claude Code:
// {"type": "prompt", "prompt": "Is the work finished? $ARGUMENTS"}. The
// model answers {"ok": true} or {"ok": false, "reason": "…"}; ok false
// blocks the way exit code 2 does for that event (a Stop hook keeps the
// agent working, a PreToolUse hook denies the call).

// Evaluator asks model (a provider/model, a role, or "" for the default)
// the system and user prompts and returns its reply.
type Evaluator func(ctx context.Context, model, system, prompt string) (string, error)

// SetEvaluator sets how prompt hooks reach a model.
func (r *Runner) SetEvaluator(e Evaluator) {
	r.mu.Lock()
	r.evaluate = e
	r.mu.Unlock()
}

const promptTimeout = 30 * time.Second

const evaluatorSystem = `You evaluate a hook in a coding agent: a check the user configured to run at one point in the agent's work. You get the user's instructions for the check and the hook input, as JSON, describing what the agent is doing (for example the tool it wants to call, or that it is about to stop).

Decide per the instructions. Reply with a single JSON object and nothing else:
{"ok": true} to let the agent go on as it is, or
{"ok": false, "reason": "..."} to stop it, where reason tells the agent, in a sentence or two, what to do instead or what is still missing.`

func (r *Runner) evalPrompt(ctx context.Context, ev Event, c Command, payload []byte) Result {
	r.mu.Lock()
	evaluate := r.evaluate
	r.mu.Unlock()
	if evaluate == nil || strings.TrimSpace(c.Prompt) == "" {
		return Result{Messages: []string{fmt.Sprintf("%s prompt hook skipped: no prompt or no model to ask", ev)}}
	}
	timeout := promptTimeout
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := c.Prompt
	if strings.Contains(prompt, "$ARGUMENTS") {
		prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", string(payload))
	} else {
		prompt += "\n\nHook input:\n" + string(payload)
	}
	reply, err := evaluate(ctx, c.Model, evaluatorSystem, prompt)
	if err != nil {
		if ctx.Err() != nil {
			return Result{Messages: []string{fmt.Sprintf("%s prompt hook timed out after %s", ev, timeout)}}
		}
		return Result{Messages: []string{fmt.Sprintf("%s prompt hook failed: %v", ev, err)}}
	}
	ok, reason, err := parseVerdict(reply)
	if err != nil {
		return Result{Messages: []string{fmt.Sprintf("%s prompt hook: %v", ev, err)}}
	}
	if ok {
		return Result{}
	}
	if reason == "" {
		reason = "blocked by " + string(ev) + " prompt hook"
	}
	res := Result{Block: true, Reason: reason}
	if ev == PreToolUse {
		res.Permission = "deny"
	}
	return res
}

// parseVerdict reads the model's JSON answer, allowing prose or a code
// fence around it, and the older {"decision": "approve"|"block"} form.
func parseVerdict(reply string) (ok bool, reason string, err error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return false, "", fmt.Errorf("the model didn't answer with JSON: %q", short(reply))
	}
	var v struct {
		OK       *bool  `json:"ok"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &v); err != nil {
		return false, "", fmt.Errorf("the model's answer isn't valid JSON: %v", err)
	}
	switch {
	case v.OK != nil:
		return *v.OK, strings.TrimSpace(v.Reason), nil
	case v.Decision == "approve":
		return true, "", nil
	case v.Decision == "block":
		return false, strings.TrimSpace(v.Reason), nil
	}
	return false, "", fmt.Errorf(`the model's answer has no "ok": %q`, short(reply))
}

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

var (
	storeCall = regexp.MustCompile(`(^|[^\w.$])store\s*\(`)
	loadCall  = regexp.MustCompile(`(^|[^\w.$])load\s*\(`)
)

// scriptState follows the successful run_code scripts of a run, to check
// that a value was stored by one and loaded by a later, different one.
type scriptState struct {
	scripts int
	stored  int // the first script that called store, or 0
	loaded  bool
}

func (s *scriptState) see(code string) {
	s.scripts++
	if s.stored > 0 && s.scripts > s.stored && loadCall.MatchString(code) {
		s.loaded = true
	}
	if s.stored == 0 && storeCall.MatchString(code) {
		s.stored = s.scripts
	}
}

// Result is one task run against one model.
type Result struct {
	Task      string
	Model     string
	Execution tools.Execution
	Pass      bool
	Detail    string // why it failed, or Setup/agent error
	CostUSD   float64
	Duration  time.Duration
	// ToolCalls counts every tool call, including those run_code scripts
	// make; Requests counts model requests.
	ToolCalls int
	Requests  int
	// Faults counts, by kind, the calls the model formed so badly they
	// never reached a tool, and FaultCalls their total — a share of
	// ToolCalls. They measure how reliably the model emits usable tool
	// calls, which a pass rate alone hides: a model that fumbles a third
	// of its calls and recovers still passes, more slowly and for more
	// tokens. This is the number to watch when changing sampling.
	Faults     map[agent.ToolFault]int
	FaultCalls int
	// Stop is why the run ended, as the agent reported it: "end_turn" when
	// the model stopped on its own, "max_tokens" when it ran past its
	// output budget, "refusal", "max_turns" at the turn cap, "loop" when
	// the loop guard caught it repeating, "interrupted" for the timeout,
	// or "error".
	Stop string
	// Error is the last error the agent itself reported, such as the
	// provider's message for a failed request; a subagent's are left out.
	// It says why a run that ended in "error" did, which Detail (the
	// verifier's view of the unfinished work) doesn't.
	Error string
	// Usage is the tokens over all requests; PeakContext is the largest
	// prompt a single request sent.
	Usage       llm.Usage
	PeakContext int
	// Compaction fields are populated by context-retention tasks. Retained
	// is the number of important facts found in the summary itself; the task
	// passes only when every fact is retained and the recovery answer is exact.
	Compactions           int
	CompactionSavedTokens int
	CompactionMeasured    bool
	Retained              int
	RetentionTotal        int
	// Kept is the directory a failed run was kept in (Options.KeepFailed):
	// work/ is the fixture as the agent left it, transcript.md the
	// conversation and calls.jsonl every tool call, scripts' included.
	Kept string
}

// Options tune a run.
type Options struct {
	Execution tools.Execution // empty means tools
	Timeout   time.Duration
	// KeepFailed keeps a failed run's directory and transcript instead of
	// deleting it, to see why it failed.
	KeepFailed bool
}

// Run sets up task in a fresh temporary directory, gives its prompt to a
// new agent running provider/model with every tool allowed (the fixture
// directory is thrown away afterwards, so there is nothing to protect),
// then verifies the outcome. It never returns an error: a setup or agent
// failure comes back as a failing Result with Detail explaining why, so a
// whole run of several models and tasks doesn't stop on one bad model.
func Run(ctx context.Context, task Task, provider llm.Provider, model string, timeout time.Duration) Result {
	return RunWith(ctx, task, provider, model, Options{Timeout: timeout})
}

// RunWith is Run with options: an execution setting, to compare the tool
// chain with run_code scripts on the same task, and keeping failures.
func RunWith(ctx context.Context, task Task, provider llm.Provider, model string, o Options) (res Result) {
	exec, _ := tools.ParseExecution(string(o.Execution))
	res = Result{Task: task.Name, Model: model, Execution: exec}
	root, err := os.MkdirTemp("", "larik-bench-")
	if err != nil {
		res.Detail = "creating the fixture directory: " + err.Error()
		return res
	}
	// The agent works in root/work, so the transcript beside it stays out
	// of the model's view.
	dir := filepath.Join(root, "work")
	keep := false
	defer func() {
		if keep {
			res.Kept = root
		} else {
			os.RemoveAll(root)
		}
	}()
	if err := os.Mkdir(dir, 0o755); err != nil {
		res.Detail = "creating the fixture directory: " + err.Error()
		return res
	}
	if err := task.Setup(dir); err != nil {
		res.Detail = "setting up the fixture: " + err.Error()
		return res
	}

	var sess *session.Session
	if o.KeepFailed {
		if sess, err = session.Create(filepath.Join(root, "session"), session.Meta{Cwd: dir, Provider: provider.Name(), Model: model}); err != nil {
			res.Detail = "creating the transcript: " + err.Error()
			return res
		}
	}
	registry := tools.Default()
	if task.compaction != nil {
		// The recovery answer must come from the summary alone. In particular,
		// --keep-failed gives the compaction wrapper a transcript path for the
		// saved artifact; no tools means the model cannot use it as a back door.
		registry = tools.NewRegistry()
	}
	a := agent.New(agent.Options{
		Provider:  provider,
		Model:     model,
		Cwd:       dir,
		MaxTurns:  40,
		Tools:     registry,
		Execution: exec,
		Perms:     permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir),
		Session:   sess,
	})

	runCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	var calls []callRecord
	var finalText string
	var state scriptState
	consume := func(e agent.Event) {
		switch e.Kind {
		case agent.EvToolEnd:
			if e.ToolName == tools.CodeToolName && task.requireScriptState && !e.IsError {
				var in struct {
					Code string `json:"code"`
				}
				if json.Unmarshal(e.Input, &in) == nil {
					state.see(in.Code)
				}
			}
			if e.ToolName != tools.CodeToolName {
				res.ToolCalls++
			}
			if e.Fault != "" {
				if res.Faults == nil {
					res.Faults = map[agent.ToolFault]int{}
				}
				res.Faults[e.Fault]++
				res.FaultCalls++
			}
			if o.KeepFailed {
				calls = append(calls, callRecord{ID: e.ToolID, Tool: e.ToolName, Input: e.Input, Output: tools.Truncate(e.Output, 4000), IsError: e.IsError})
			}
		case agent.EvUsage:
			res.Requests++
			if e.Usage != nil {
				res.PeakContext = max(res.PeakContext, e.Usage.Turn.ContextTokens())
			}
		case agent.EvPermission:
			e.Reply <- agent.PermissionReply{Allow: true}
		case agent.EvAssistant:
			if e.Message != nil {
				finalText = e.Message.Text()
			}
		case agent.EvError:
			if e.Agent == "" {
				res.Error = e.Text
			}
		case agent.EvDone:
			// Subagents report their own; the last one is the run's.
			if e.Agent == "" {
				res.Stop = e.StopReason
			}
		}
	}

	start := time.Now()
	if c := task.compaction; c != nil {
		res.RetentionTotal = len(c.summaryChecks)
		if sess != nil {
			for _, m := range c.messages {
				if err := sess.AppendMessage(m, nil); err != nil {
					res.Detail = "seeding the transcript: " + err.Error()
					break
				}
			}
		}
		if res.Detail == "" {
			a.Restore(&session.State{Messages: c.messages})
			summary, info, err := a.Compact(runCtx, consume)
			res.CompactionMeasured = info.Available
			res.CompactionSavedTokens = info.SavedTokens
			if err != nil {
				res.Detail = "compacting the synthetic conversation: " + err.Error()
			} else {
				res.Retained, _ = scoreSummary(summary, c.summaryChecks)
				for e := range a.Run(runCtx, task.Prompt) {
					consume(e)
				}
				if runCtx.Err() == nil {
					res.Pass, res.Detail = verifyRecovery(finalText, c.want)
					if res.Retained != res.RetentionTotal {
						_, missing := scoreSummary(summary, c.summaryChecks)
						res.Pass = false
						res.Detail = strings.TrimSpace(res.Detail + "\nsummary omitted: " + strings.Join(missing, ", "))
					}
				}
			}
		}
	} else {
		for e := range a.Run(runCtx, task.Prompt) {
			consume(e)
		}
		if runCtx.Err() == nil {
			res.Pass, res.Detail = task.Verify(dir)
			if res.Pass && task.requireScriptState && exec != tools.ExecTools && !state.loaded {
				res.Pass = false
				res.Detail = "run_code was available, but store and load were not used in separate scripts"
			}
		}
	}
	res.Duration = time.Since(start)
	stats := a.Stats()
	res.CostUSD, res.Usage, res.Compactions = stats.CostUSD, stats.Total, stats.Compactions

	if runCtx.Err() != nil {
		res.Pass = false
		res.Detail = fmt.Sprintf("timed out after %s", o.Timeout)
	}
	if sess != nil {
		sess.Close()
		if !res.Pass {
			keep = true
			if err := writeRecord(root, sess, calls, res.Error); err != nil {
				res.Detail += "\n(keeping the transcript failed: " + err.Error() + ")"
			}
		}
	}
	return res
}

// callRecord is one line of calls.jsonl.
type callRecord struct {
	ID      string          `json:"id"`
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output"`
	IsError bool            `json:"is_error,omitempty"`
}

// writeRecord saves a failed run's conversation as Markdown, ending with
// runErr, the agent's error if it had one, and its tool calls, including
// the ones scripts made, as JSON lines.
func writeRecord(root string, sess *session.Session, calls []callRecord, runErr string) error {
	st, err := session.Load(sess.Path)
	if err != nil {
		return err
	}
	transcript := session.Markdown(st, sess.ID)
	if runErr != "" {
		transcript = strings.TrimRight(transcript, "\n") + "\n\n## Error\n\n```\n" + runErr + "\n```\n"
	}
	if err := os.WriteFile(filepath.Join(root, "transcript.md"), []byte(transcript), 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(root, "calls.jsonl"))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, c := range calls {
		if err := enc.Encode(c); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// faultOrder lists the fault kinds in a fixed order, so a report reads the
// same way every run.
var faultOrder = []agent.ToolFault{
	agent.FaultInvalidJSON,
	agent.FaultInvalidArguments,
	agent.FaultUnknownTool,
	agent.FaultBadCallTool,
}

// FaultBreakdown renders the fault kinds that occurred, commonest first in
// the fixed order above, e.g. "invalid_json 2, unknown_tool 1". It is empty
// when the model formed every call usably.
func (r Result) FaultBreakdown() string {
	var parts []string
	for _, f := range faultOrder {
		if n := r.Faults[f]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", f, n))
		}
	}
	return strings.Join(parts, ", ")
}

// FaultRate is the share of tool calls the model formed unusably, 0 when it
// made none.
func (r Result) FaultRate() float64 {
	if r.ToolCalls == 0 {
		return 0
	}
	return float64(r.FaultCalls) / float64(r.ToolCalls)
}

// stoppedTalking lists the stop reasons that mean the model ended its turn
// by producing prose instead of a tool call: it finished deliberately, or
// rambled past its output budget, or declined. A turn cap, a timeout, an
// error or the loop guard are not the model choosing to stop.
var stoppedTalking = map[string]bool{"end_turn": true, "max_tokens": true, "refusal": true}

// StoppedWithoutActing reports a run the model ended by talking rather than
// calling a tool, while the task was still unfinished. This is the failure
// bad sampling actually produces: nothing is malformed, the model simply
// never commits to a call. It is deliberately paired with Pass, because a
// run that succeeds also ends in prose — that is how an agent reports it is
// done — so the prose alone says nothing.
func (r Result) StoppedWithoutActing() bool {
	return !r.Pass && stoppedTalking[r.Stop]
}

// NeverActed is the sharper case: the model answered without calling a
// single tool. StoppedWithoutActing covers giving up partway too.
func (r Result) NeverActed() bool {
	return r.StoppedWithoutActing() && r.ToolCalls == 0
}

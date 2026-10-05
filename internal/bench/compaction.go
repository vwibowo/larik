package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"larik/internal/llm"
)

// compactionCase is a mechanically scored, synthetic long conversation. The
// model first summarizes Messages through Agent.Compact, then answers Prompt
// from the compacted context alone. summaryChecks show what the summary kept;
// recovery proves the information remained usable after compaction.
type compactionCase struct {
	messages      []llm.Message
	summaryChecks []summaryCheck
	want          recoveryAnswer
}

type summaryCheck struct {
	name      string
	fragments []string
}

type recoveryAnswer struct {
	Project          string `json:"project"`
	Ticket           string `json:"ticket"`
	Storage          string `json:"storage"`
	IngestPort       int    `json:"ingest_port"`
	File             string `json:"file"`
	Function         string `json:"function"`
	MaxBatch         int    `json:"max_batch"`
	RejectedApproach string `json:"rejected_approach"`
	RemainingTodo    string `json:"remaining_todo"`
	NextAction       string `json:"next_action"`
}

func compactionRetentionTask() Task {
	want := recoveryAnswer{
		Project:          "Northstar",
		Ticket:           "ACME-4821",
		Storage:          "SQLite WAL",
		IngestPort:       4317,
		File:             "internal/relay/buffer.go",
		Function:         "FlushPending",
		MaxBatch:         64,
		RejectedApproach: "global mutex",
		RemainingTodo:    "add crash-recovery test",
		NextAction:       "wire FlushPending into shutdown hook",
	}
	c := &compactionCase{
		want: want,
		summaryChecks: []summaryCheck{
			{"project", []string{"Northstar"}},
			{"ticket", []string{"ACME-4821"}},
			{"storage", []string{"SQLite", "WAL"}},
			{"ingest port", []string{"4317"}},
			{"file", []string{"internal/relay/buffer.go"}},
			{"function", []string{"FlushPending"}},
			{"batch limit", []string{"64"}},
			{"rejected approach", []string{"global mutex"}},
			{"remaining todo", []string{"crash-recovery test"}},
			{"next action", []string{"FlushPending", "shutdown hook"}},
		},
	}
	c.messages = compactionConversation()
	return Task{
		Name:       "compaction-retention",
		Prompt:     "Using only the compacted conversation, return one JSON object with exactly these keys and no Markdown: project, ticket, storage, ingest_port, file, function, max_batch, rejected_approach, remaining_todo, next_action. Use concise values, preserve identifiers and numbers exactly, and do not add keys.",
		Setup:      func(string) error { return nil },
		Verify:     func(string) (bool, string) { return false, "compaction benchmark uses its recovery response" },
		compaction: c,
	}
}

func compactionConversation() []llm.Message {
	return []llm.Message{
		llm.UserText("We are starting project Northstar for customer ticket ACME-4821. The first draft mentioned ingest port 4318, but that number is provisional; do not treat it as final."),
		assistantText("Understood. I will keep the project and ticket attached to the implementation notes.\n\n" + syntheticNoise("discovery", 90)),
		llm.UserText("Decision update: the final ingest port is 4317, replacing 4318. Use SQLite in WAL mode for storage, not PostgreSQL. The hard batch limit is 64 records."),
		assistantText("Recorded the corrected port, storage decision, and limit. The implementation lives in internal/relay/buffer.go and the key function is FlushPending.\n\n" + syntheticNoise("build", 90)),
		llm.UserText("The global mutex approach was rejected because it stalled readers. Keep that failed approach in the history; the sharded queue is the accepted direction."),
		assistantText("The rejected global mutex and accepted sharded queue are distinguished in the notes.\n\n" + syntheticNoise("test", 90)),
		llm.UserText("One item remains: add crash-recovery test. The immediate next action is to wire FlushPending into shutdown hook. Preserve those phrases so another engineer can continue."),
		assistantText("I marked the handoff ready. The decisions and corrections remain distributed through the discussion above rather than copied into one final checklist."),
	}
}

func assistantText(s string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock(s)}}
}

// syntheticNoise makes retention compete with realistic but irrelevant command
// output without checking a giant fixture into the repository.
func syntheticNoise(stage string, lines int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "%s shard=%02d case=%03d duration=%dms status=ok retries=0 bytes=%d\n", stage, i%12, i, 10+i%17, 2048+i*13)
	}
	return b.String()
}

func scoreSummary(summary string, checks []summaryCheck) (retained int, missing []string) {
	for _, check := range checks {
		ok := true
		for _, fragment := range check.fragments {
			if !strings.Contains(summary, fragment) {
				ok = false
				break
			}
		}
		if ok {
			retained++
		} else {
			missing = append(missing, check.name)
		}
	}
	return retained, missing
}

func verifyRecovery(text string, want recoveryAnswer) (bool, string) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "```json"), "```"))
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "```"), "```"))
	}
	var got recoveryAnswer
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		return false, "recovery response is not the requested JSON: " + err.Error()
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return false, "recovery response contains more than one JSON value"
		}
		return false, "recovery response has trailing content: " + err.Error()
	}
	// Recovery is about whether each fact remains usable, not whether the model
	// copies one canonical sentence byte-for-byte. Natural expansions and
	// capitalization such as "SQLite in WAL mode" or "Global mutex" still
	// retain the fact. Unknown JSON keys were rejected above, and numeric
	// corrections remain exact.
	var missing []string
	require := func(name, value string, fragments ...string) {
		value = strings.ToLower(value)
		for _, fragment := range fragments {
			if !strings.Contains(value, strings.ToLower(fragment)) {
				missing = append(missing, name)
				return
			}
		}
	}
	require("project", got.Project, want.Project)
	require("ticket", got.Ticket, want.Ticket)
	require("storage", got.Storage, "SQLite", "WAL")
	require("file", got.File, want.File)
	require("function", got.Function, want.Function)
	require("rejected_approach", got.RejectedApproach, "global mutex")
	require("remaining_todo", got.RemainingTodo, "crash-recovery test")
	require("next_action", got.NextAction, "FlushPending", "shutdown hook")
	if got.IngestPort != want.IngestPort {
		missing = append(missing, "ingest_port")
	}
	if got.MaxBatch != want.MaxBatch {
		missing = append(missing, "max_batch")
	}
	if len(missing) > 0 {
		return false, "recovery omitted or changed: " + strings.Join(missing, ", ")
	}
	return true, ""
}

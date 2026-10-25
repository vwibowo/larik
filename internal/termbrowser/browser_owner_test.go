package termbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"larik/internal/tools"
)

func TestResolveTabOwners(t *testing.T) {
	tests := []struct {
		name        string
		openers     map[target.ID]target.ID
		known, want map[target.ID]string
	}{
		{"trace descendants", map[target.ID]target.ID{"trace": "", "popup": "trace", "grandchild": "popup"}, map[target.ID]string{"trace": "trace"}, map[target.ID]string{"trace": "trace", "popup": "trace", "grandchild": "trace"}},
		{"known ownership wins", map[target.ID]target.ID{"trace": "", "main": "trace", "child": "trace", "popup": "child", "main-popup": "main"}, map[target.ID]string{"trace": "trace", "main": "", "child": "child"}, map[target.ID]string{"trace": "trace", "main": "", "child": "child", "popup": "child", "main-popup": ""}},
		{"ownerless", map[target.ID]target.ID{"blank": "", "orphan": "missing", "descendant": "orphan"}, nil, map[target.ID]string{"blank": "", "orphan": "", "descendant": "", "missing": ""}},
		{"cycles", map[target.ID]target.ID{"a": "b", "b": "a", "descendant": "a", "self": "self"}, nil, map[target.ID]string{"a": "", "b": "", "descendant": "", "self": ""}},
		{"known cycle", map[target.ID]target.ID{"a": "b", "b": "a", "descendant": "b"}, map[target.ID]string{"a": "trace"}, map[target.ID]string{"a": "trace", "b": "trace", "descendant": "trace"}},
		{"closed known opener", map[target.ID]target.ID{"popup": "closed", "grandchild": "popup"}, map[target.ID]string{"closed": "trace"}, map[target.ID]string{"closed": "trace", "popup": "trace", "grandchild": "trace"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var infos []*target.Info
			for id, opener := range tt.openers {
				infos = append(infos, &target.Info{TargetID: id, OpenerID: opener, Type: "page"})
			}
			// Exhaust every target ordering, including grandchild before parent.
			var check func(int)
			check = func(i int) {
				if i == len(infos) {
					if got := resolveTabOwners(infos, tt.known); !reflect.DeepEqual(got, tt.want) {
						t.Fatalf("owners = %v, want %v", got, tt.want)
					}
					return
				}
				for j := i; j < len(infos); j++ {
					infos[i], infos[j] = infos[j], infos[i]
					check(i + 1)
					infos[i], infos[j] = infos[j], infos[i]
				}
			}
			check(0)
		})
	}
}

// This is browser tool API isolation, not a separate Chrome profile or an OS
// security boundary. Exercise the same Run entry points available to agents.
func TestBrowserToolsTraceOwnerIsolation(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	trace := tools.WithOwner(ctx, "trace")
	if err := s.OpenURL(ctx, "trace", srv.URL+"/other?trace-private"); err != nil {
		t.Fatal(err)
	}
	eval := func(ctx context.Context, expression string) tools.Result {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"expression": expression})
		r := (EvalTool{s}).Run(ctx, nil, input)
		if r.IsError {
			t.Fatal(r.Content)
		}
		return r
	}
	eval(trace, "document.title='TRACE_PRIVATE'; document.body.textContent='TRACE_PRIVATE'; console.log('TRACE_PRIVATE_LOG'); 'ok'")
	for _, owner := range []string{"", "child"} {
		t.Run(fmt.Sprintf("owner=%q", owner), func(t *testing.T) {
			own := tools.WithOwner(ctx, owner)
			if r := (NavigateTool{s}).Run(own, nil, json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL+"/"))); r.IsError {
				t.Fatal(r.Content)
			}
			eval(own, "console.log('OWN_LOG'); 'ok'")
			for _, tool := range []tools.Tool{TabsTool{s}, SnapshotTool{s}, EvalTool{s}, NetworkTool{s}, ConsoleTool{s}} {
				input := json.RawMessage("{}")
				switch tool.(type) {
				case TabsTool:
					input = json.RawMessage(`{"action":"list"}`)
				case EvalTool:
					input = json.RawMessage(`{"expression":"document.title"}`)
				}
				r := tool.Run(own, nil, input)
				if r.IsError || strings.Contains(r.Content, "TRACE_PRIVATE") || strings.Contains(r.Content, "trace-private") {
					t.Fatalf("%T leaked or failed: %+v", tool, r)
				}
				if _, ok := tool.(ConsoleTool); ok && !strings.Contains(r.Content, "OWN_LOG") {
					t.Fatalf("own console missing: %s", r.Content)
				}
			}
			for _, action := range []string{"select", "close"} {
				for _, index := range []int{-1, 1, 100} {
					r := (TabsTool{s}).Run(own, nil, json.RawMessage(fmt.Sprintf(`{"action":%q,"index":%d}`, action, index)))
					if !r.IsError {
						t.Fatalf("%s %d accepted: %s", action, index, r.Content)
					}
				}
			}
			for _, input := range []string{`{"action":"new"}`, `{"action":"select","index":0}`, `{"action":"close","index":1}`} {
				if r := (TabsTool{s}).Run(own, nil, json.RawMessage(input)); r.IsError || strings.Contains(r.Content, "TRACE_PRIVATE") {
					t.Fatalf("tabs %s: %+v", input, r)
				}
			}
			if r := eval(own, "document.title"); !strings.Contains(r.Content, "Test form") {
				t.Fatalf("wrong active tab: %s", r.Content)
			}
		})
	}
	if r := (SnapshotTool{s}).Run(trace, nil, nil); r.IsError || !strings.Contains(r.Content, "TRACE_PRIVATE") {
		t.Fatalf("trace damaged: %+v", r)
	}
	if r := (NetworkTool{s}).Run(trace, nil, nil); r.IsError || !strings.Contains(r.Content, "trace-private") {
		t.Fatalf("trace network missing: %+v", r)
	}
	if r := (ConsoleTool{s}).Run(trace, nil, nil); r.IsError || !strings.Contains(r.Content, "TRACE_PRIVATE_LOG") {
		t.Fatalf("trace console drained: %+v", r)
	}
}

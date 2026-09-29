package viewer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/trace"
)

func sample(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s.trace")
	rec, err := trace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr := rec.Tracer()
	tr.TurnStart("say </script><script>alert(1)</script>", "", nil)
	id := tr.Request("fake", llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}}, "")
	tr.RecordHTTP(&llm.WireExchange{Req: id, Method: "POST", URL: "https://x", ReqBody: []byte(`{"a":1}`), RespBody: []byte("data: ok")})
	tr.Response(id, trace.Response{DurationMS: 5})
	rec.Close()
	return dir
}

func TestServer(t *testing.T) {
	dir := sample(t)
	s, err := Start(Options{Dir: dir, Title: "s"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	get := func(url string) (int, string) {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	base := strings.TrimSuffix(s.URL(), "/?t="+s.token)
	if code, _ := get(base + "/api/events?after=0"); code != http.StatusForbidden {
		t.Fatalf("no token: %d", code)
	}
	if code, body := get(s.URL()); code != 200 || !strings.Contains(body, `"mode":"live"`) {
		t.Fatalf("page: %d", code)
	}
	code, body := get(base + "/api/events?after=1&t=" + s.token)
	var got struct {
		Records []trace.Record
		Live    bool
	}
	json.Unmarshal([]byte(body), &got)
	if code != 200 || len(got.Records) != 3 || got.Records[0].Seq != 2 || !got.Live {
		t.Fatalf("events after 1: %d %s", code, body)
	}
	var name string
	for _, r := range got.Records {
		if r.Kind == trace.KindHTTP {
			name = r.Data.(map[string]any)["request_body"].(string)
		}
	}
	if code, body := get(base + "/api/body/" + name + "?t=" + s.token); code != 200 || body != `{"a":1}` {
		t.Fatalf("body: %d %q", code, body)
	}
	if code, _ := get(base + "/api/body/..%2fevents.jsonl?t=" + s.token); code != http.StatusNotFound {
		t.Fatalf("only body files are served: %d", code)
	}
	req, _ := http.NewRequest("GET", s.URL(), nil)
	req.Host = "evil.example"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("a foreign Host must be refused")
	}
}

func TestExport(t *testing.T) {
	var buf bytes.Buffer
	if err := Export(Options{Dir: sample(t), Title: "s"}, &buf); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if !strings.Contains(page, `"mode":"static"`) || !strings.Contains(page, "data: ok") {
		t.Fatal("the export should inline records and bodies")
	}
	if strings.Contains(page, "<script>alert(1)") {
		t.Fatal("trace text must not be able to end the script")
	}
}

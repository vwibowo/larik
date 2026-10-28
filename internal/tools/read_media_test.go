package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestReadImage(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(dir, "shot.PNG"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(dir)
	r := run(t, Read{}, env, `{"path":"shot.PNG"}`)
	if r.IsError || len(r.Images) != 1 || r.Images[0].Type != llm.BlockImage || r.Images[0].MediaType != "image/png" || !strings.Contains(r.Content, "<image") {
		t.Fatalf("got %+v", r)
	}
	env.mu.Lock()
	_, read := env.reads[filepath.Join(dir, "shot.PNG")]
	env.mu.Unlock()
	if !read {
		t.Error("an image read whole counts as read")
	}
}

func TestReadPDF(t *testing.T) {
	dir := t.TempDir()
	pdf := []byte("%PDF-1.4\n1 0 obj << /Type /Page >> endobj\n2 0 obj << /Type /Page >> endobj\n")
	if err := os.WriteFile(filepath.Join(dir, "spec.pdf"), pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(dir)
	if r := run(t, Read{}, env, `{"path":"spec.pdf"}`); !r.IsError || !strings.Contains(r.Content, "can't read documents") {
		t.Fatalf("without document support: %+v", r)
	}
	env.AcceptsDocument = func(mediaType string) bool { return mediaType == "application/pdf" }
	r := run(t, Read{}, env, `{"path":"spec.pdf"}`)
	if r.IsError || len(r.Images) != 1 || r.Images[0].Type != llm.BlockDocument || r.Images[0].Pages != 2 {
		t.Fatalf("got %+v", r)
	}
}

func TestReadImageTooLarge(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.jpg"), make([]byte, maxReadImageBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := run(t, Read{}, NewEnv(dir), `{"path":"big.jpg"}`); !r.IsError || !strings.Contains(r.Content, "limited to 5 MB") {
		t.Fatalf("got %+v", r)
	}
}

// mediaCaller answers every call with one image.
type mediaCaller struct{}

func (mediaCaller) CallTool(context.Context, string, json.RawMessage) Result {
	return Result{Content: "<image/>", Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"}}}
}

func TestScriptPassesImagesOn(t *testing.T) {
	r := runScript(t, context.Background(), NewEnv(t.TempDir()), mediaCaller{},
		`for (let i = 0; i < 10; i++) console.log(tools.call("echo", {text: "x"}))`)
	if r.IsError || len(r.Images) != maxScriptMedia {
		t.Fatalf("images %d, result %+v", len(r.Images), r.Content)
	}
	for _, want := range []string{"8 image(s) or document(s) from the script's calls are attached", "2 more left out", "scripts get text only"} {
		if !strings.Contains(r.Content, want) {
			t.Errorf("result lacks %q:\n%s", want, r.Content)
		}
	}
}

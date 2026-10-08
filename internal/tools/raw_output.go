package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"larik/internal/llm"
	"larik/internal/session"
)

type callIDKey struct{}

// WithCallID identifies a command's exact output without trusting shell text.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallID is the id of the tool call ctx runs, or "".
func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// rawOutputFile creates the file raw_output reads for the call id.
func (e *Env) rawOutputFile(id string) (*os.File, error) {
	if err := os.MkdirAll(e.RawOutputDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(e.RawOutputDir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(e.RawOutputDir, session.RawName(id)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

// RawOutput reads a bounded byte range of a prior bash or run_code call's
// exact output.
type RawOutput struct{}

func (RawOutput) ReadOnly() bool { return true }
func (RawOutput) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "raw_output",
		Description: "Read exact output from a previous bash or run_code call by tool_call_id. Offset is a zero-based byte position; limit defaults to 20000 bytes (max 20000). Never reruns the command.",
		Schema:      schema(`{"type":"object","properties":{"tool_call_id":{"type":"string"},"offset":{"type":"integer","description":"Zero-based byte offset (default 0)"},"limit":{"type":"integer","description":"Bytes to return (default and max 20000)"}},"required":["tool_call_id"]}`),
	}
}

func (RawOutput) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		ID     string `json:"tool_call_id"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	if in.ID == "" || env.RawOutputDir == "" || in.Offset < 0 {
		return errorf("raw output is unavailable or offset is negative")
	}
	if in.Limit <= 0 || in.Limit > 20000 {
		in.Limit = 20000
	}
	path := filepath.Join(env.RawOutputDir, session.RawName(in.ID))
	f, err := os.Open(path)
	if err != nil {
		return errorf("raw output for %q is unavailable", in.ID)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return errorf("reading raw output: %v", err)
	}
	if fi.Size() == 0 {
		return Result{Content: "(empty output)"}
	}
	if in.Offset >= fi.Size() {
		return errorf("offset %d is past the end of the output (%d bytes)", in.Offset, fi.Size())
	}
	if err := ctx.Err(); err != nil {
		return errorf("%v", err)
	}
	chunk := make([]byte, min(int64(in.Limit), fi.Size()-in.Offset))
	n, err := f.ReadAt(chunk, in.Offset)
	if err != nil && err != io.EOF {
		return errorf("reading raw output: %v", err)
	}
	chunk = chunk[:n]
	encoding, content := "text", string(chunk)
	if !utf8.Valid(chunk) || bytes.IndexByte(chunk, 0) >= 0 {
		encoding, content = "base64", base64.StdEncoding.EncodeToString(chunk)
	}
	next := in.Offset + int64(n)
	return Result{Content: fmt.Sprintf("bytes %d-%d of %d (%s):\n%s\n[next_offset=%d]", in.Offset, next, fi.Size(), encoding, content, next)}
}

// Command server is a tiny MCP server used by the mcp package tests.
// It serves over stdio by default, streamable HTTP with -http addr, or the
// older SSE transport with -sse addr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address")
	sseAddr := flag.String("sse", "", "serve the SSE transport on this address")
	flag.Parse()
	fmt.Fprintln(os.Stderr, "test server starting; greeting="+os.Getenv("GREETING")+" args="+strings.Join(flag.Args(), ","))
	s := NewServer()
	if *sseAddr != "" {
		log.Fatal(http.ListenAndServe(*sseAddr, mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s }, nil)))
	}
	if *httpAddr != "" {
		log.Fatal(http.ListenAndServe(*httpAddr, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)))
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

type echoIn struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

func NewServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	closed := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        "echo",
		Description: "Echo text back",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv("GREETING") + in.Text}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fails"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}}, nil, nil
		})
	s.AddResource(&mcp.Resource{URI: "note://readme", Name: "readme", Description: "The project notes", MIMEType: "text/plain"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "remember the milk"}}}, nil
		})
	// A 1x1 PNG.
	dot := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	s.AddResource(&mcp.Resource{URI: "img://dot", Name: "dot", MIMEType: "image/png"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "image/png", Blob: dot}}}, nil
		})
	s.AddPrompt(&mcp.Prompt{Name: "review", Description: "Review a file", Arguments: []*mcp.PromptArgument{
		{Name: "file", Required: true}, {Name: "focus"},
	}}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		a := req.Params.Arguments
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: "Review " + a["file"] + " focusing on " + a["focus"] + "."}},
		}}, nil
	})
	return s
}

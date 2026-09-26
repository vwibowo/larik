// Command server is a tiny MCP server used by the mcp package tests.
// It serves over stdio by default, or streamable HTTP with -http addr.
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
	flag.Parse()
	fmt.Fprintln(os.Stderr, "test server starting; greeting="+os.Getenv("GREETING")+" args="+strings.Join(flag.Args(), ","))
	s := NewServer()
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
	return s
}

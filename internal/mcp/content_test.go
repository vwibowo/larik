package mcp

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRenderContentPassesImages(t *testing.T) {
	var content []sdk.Content
	content = append(content, &sdk.TextContent{Text: "Took a screenshot"})
	for range maxToolImages + 1 {
		content = append(content, &sdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}})
	}
	content = append(content, &sdk.ImageContent{MIMEType: "image/tiff", Data: []byte{1}})

	text, images := renderContent(&sdk.CallToolResult{Content: content})
	if len(images) != maxToolImages {
		t.Fatalf("got %d images, want %d", len(images), maxToolImages)
	}
	if images[0].MediaType != "image/png" || images[0].Data != "AQID" {
		t.Errorf("image: %+v", images[0])
	}
	for _, want := range []string{"Took a screenshot", "[image 1 attached]", "[image image/png, 3 bytes, not shown]", "[image image/tiff, 1 bytes, not shown]"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

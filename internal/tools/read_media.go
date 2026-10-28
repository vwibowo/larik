package tools

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"larik/internal/llm"
)

// readImageTypes are the images read returns for the model to look at,
// by extension; the same set a prompt can attach with @.
var readImageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

const (
	maxReadImageBytes    = 5 << 20
	maxReadDocumentBytes = 32 << 20
)

// readMedia returns an image or PDF whole, as an attachment for the model,
// when path is one; ok is false for anything else, which reads as text.
func (env *Env) readMedia(path string, size int64) (res Result, ok bool) {
	ext := strings.ToLower(filepath.Ext(path))
	mediaType, image := readImageTypes[ext]
	limit := int64(maxReadImageBytes)
	switch {
	case image:
	case ext == ".pdf":
		mediaType, limit = "application/pdf", maxReadDocumentBytes
		if env.AcceptsDocument == nil || !env.AcceptsDocument(mediaType) {
			return errorf("%s is a PDF, and this model's provider can't read documents; extract its text with bash (pdftotext, if installed)", path), true
		}
	default:
		return Result{}, false
	}
	if size > limit {
		return errorf("%s is %d MB; files read whole are limited to %d MB", path, size>>20, limit>>20), true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errorf("%v", err), true
	}
	env.markRead(path)
	block := llm.Block{Type: llm.BlockImage, MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data), Attachment: path}
	kind := "image"
	if !image {
		block.Type, kind = llm.BlockDocument, "document"
		block.Pages = PDFPages(data)
	}
	return Result{Content: fmt.Sprintf("<%s path=%q size=\"%d KB\"/> (attached)", kind, path, (len(data)+1023)/1024), Images: []llm.Block{block}}, true
}

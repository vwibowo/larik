package filespane

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDuplicateFilesFolderKeepsContents(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "src")
	first, err := DuplicatePath(src)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DuplicatePath(src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "src copy" || filepath.Base(second) != "src copy 2" {
		t.Fatalf("duplicate names: %q, %q", first, second)
	}
	data, err := os.ReadFile(filepath.Join(second, "main.go"))
	if err != nil || string(data) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("duplicated contents: %q, %v", data, err)
	}
}

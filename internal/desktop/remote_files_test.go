package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteFileOperationsStayOnHost(t *testing.T) {
	needTerminal(t)
	data := t.TempDir()
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("remote notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, connect := standIn(t, data)
	dial, _, stop, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	source := remoteFiles{name: "devbox", root: root, dial: dial}
	call := func(op, path, name string) remoteFileReply {
		t.Helper()
		reply, err := source.call(remoteFileRequest{Op: op, Path: remotePath("devbox", path), Name: name, Query: "notes"})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return reply
	}
	if got := call("list", root, ""); len(got.Entries) != 1 || got.Entries[0].Path != path {
		t.Fatalf("list: %+v", got)
	}
	if got := call("search", root, ""); got.Matches != 1 || len(got.Tree[root]) != 1 {
		t.Fatalf("search: %+v", got)
	}
	if got := call("preview", path, ""); got.Text != "remote notes\n" {
		t.Fatalf("preview: %+v", got)
	}
	folder := call("create-folder", root, "drafts").Dest
	file := call("create-file", folder, "first.txt").Dest
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("host did not create file: %v", err)
	}
	renamed := call("rename", file, "second.txt").Dest
	duplicate := call("duplicate", renamed, "").Dest
	if !strings.HasSuffix(duplicate, "second copy.txt") {
		t.Fatalf("duplicate: %q", duplicate)
	}
	call("trash", duplicate, "")
	if _, err := os.Stat(duplicate); !os.IsNotExist(err) {
		t.Fatalf("trash left file: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(data, "desktop", "trash")); err != nil || len(entries) != 1 {
		t.Fatalf("recoverable trash: %v, %d entries", err, len(entries))
	}
	if _, err := source.call(remoteFileRequest{Op: "preview", Path: remotePath("devbox", filepath.Dir(root))}); err == nil {
		t.Fatal("read outside explorer folder succeeded")
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := source.call(remoteFileRequest{Op: "preview", Path: remotePath("devbox", link)}); err == nil {
		t.Fatal("read followed a symlink outside explorer folder")
	}
	if _, err := source.call(remoteFileRequest{Op: "create-file", Path: remotePath("other", root), Name: "bad"}); err == nil {
		t.Fatal("request crossed hosts")
	}
}

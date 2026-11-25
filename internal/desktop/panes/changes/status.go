package changespane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type StatusEntry struct {
	Code, Path string
	From       string
}

func Fingerprint(ctx context.Context, root, indexPath string) (string, error) {
	status, err := Git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	branch, _ := Git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	head, _ := Git(ctx, root, "rev-parse", "HEAD")
	h := sha256.New()
	h.Write(status)
	h.Write(branch)
	h.Write(head)
	if info, err := os.Stat(indexPath); err == nil {
		fmt.Fprintf(h, "index:%d:%d:%v\x00", info.Size(), info.ModTime().UnixNano(), info.Mode())
	} else {
		fmt.Fprintf(h, "index:%v\x00", err)
	}
	for _, entry := range ParseStatus(status) {
		for _, path := range []string{entry.Path, entry.From} {
			if path == "" {
				continue
			}
			if info, err := os.Stat(filepath.Join(root, path)); err == nil {
				fmt.Fprintf(h, "%s:%d:%d:%v\x00", path, info.Size(), info.ModTime().UnixNano(), info.Mode())
			} else {
				fmt.Fprintf(h, "%s:%v\x00", path, err)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func SinceEntries(ctx context.Context, root, since string, status []StatusEntry) []StatusEntry {
	out, err := Git(ctx, root, "diff", "--name-status", "-z", "--no-renames", since)
	if err != nil {
		return status
	}
	var entries []StatusEntry
	fields := bytes.Split(out, []byte{0})
	for i := 0; i+1 < len(fields); i += 2 {
		if code := string(fields[i]); code != "" {
			path := string(fields[i+1])
			entries = append(entries, StatusEntry{Code: code[:1] + " ", Path: path, From: path})
		}
	}
	for _, e := range status {
		if e.Code == "??" {
			entries = append(entries, e)
		}
	}
	return entries
}

func ParseStatus(out []byte) []StatusEntry {
	var entries []StatusEntry
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := string(fields[i])
		if len(f) < 4 {
			continue
		}
		e := StatusEntry{Code: f[:2], Path: f[3:]}
		e.From = e.Path
		if e.Code[0] == 'R' || e.Code[0] == 'C' {
			if i+1 < len(fields) {
				i++
				e.From = string(fields[i])
			}
		}
		entries = append(entries, e)
	}
	return entries
}

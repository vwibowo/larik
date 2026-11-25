package changespane

import (
	"context"
	"strconv"
	"strings"
	"time"
)

func Repo(dir string) (root, indexPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := Git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	root = strings.TrimSpace(string(out))
	out, err = Git(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", ""
	}
	return root, strings.TrimSpace(string(out))
}

func IndexBlobs(ctx context.Context, root string) map[string]string {
	out, err := Git(ctx, root, "ls-files", "-s", "-z")
	if err != nil {
		return nil
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		// "<mode> <blob> <stage>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[2] == "0" {
			blobs[path] = f[1]
		}
	}
	return blobs
}

func Numstat(ctx context.Context, root, since string) map[string][2]int {
	out, err := Git(ctx, root, "diff", since, "--numstat", "--no-renames")
	if err != nil {
		return nil
	}
	counts := map[string][2]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		adds, _ := strconv.Atoi(f[0])
		dels, _ := strconv.Atoi(f[1])
		counts[f[2]] = [2]int{adds, dels}
	}
	return counts
}

func IsBinary(s string) bool {
	return strings.IndexByte(s[:min(len(s), 8000)], 0) >= 0
}

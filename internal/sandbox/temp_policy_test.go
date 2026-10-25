package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestCustomTMPDIRDoesNotGrantItsParent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin temp-root policy")
	}
	temp := filepath.Join(t.TempDir(), "custom")
	t.Setenv("TMPDIR", temp)
	for _, path := range defaultWritable(t.TempDir()) {
		if real(path) == real(filepath.Dir(temp)) || real(path) == real("/tmp") || real(path) == "/" {
			t.Fatalf("overbroad writable path from TMPDIR: %s", path)
		}
	}
}

func TestSecretControlFilesCannotBeChanged(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "cli-config.json")
	if err := os.WriteFile(secret, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sb, root, _ := newTest(t, Config{SecretPaths: []string{secret}})
	if out, err := run(t, sb, root, "echo tampered > "+strconv.Quote(secret)); err == nil && runtime.GOOS == "darwin" {
		t.Fatalf("secret file writable: %s", out)
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "original" {
		t.Fatalf("secret file changed: %q %v", data, err)
	}
}

// On a case-insensitive disk (macOS default) git and Larik open
// .git/COMMONDIR and .LARIK as the protected lowercase names, so a
// differently-cased spelling must be refused too.
func TestProtectedPathsAreNotBypassedByCase(t *testing.T) {
	sb, root, _ := newTest(t, Config{})
	probe := filepath.Join(root, "CaseProbe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "caseprobe")); err != nil {
		t.Skip("case-sensitive file system: other spellings are other files")
	}
	for _, cmd := range []string{
		"echo /tmp/x > .git/COMMONDIR",
		"echo x > .git/CONFIG",
		"echo x > .git/HOOKS/pre-commit",
		"mkdir .LARIK && echo {} > .LARIK/settings.json",
		"echo {} > .MCP.JSON",
	} {
		if out, err := run(t, sb, root, cmd); err == nil {
			t.Errorf("%q succeeded in the sandbox: %s", cmd, out)
		}
	}
}

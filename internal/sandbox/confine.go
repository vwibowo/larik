package sandbox

import (
	"os"
	"strings"
)

// Confine wraps the command line argv (argv[0] an absolute path) so that
// it runs far more confined than a bash command: it can't write anywhere,
// has no network at all (not even localhost), can't start other programs,
// and can't read personal files (home directories, temp directories,
// mounted volumes); everything else is read-only. It talks to its parent
// only over the pipes it inherits. It is meant for Larik's own helpers,
// such as the run_code script runner, which need nothing more.
func (s *Sandbox) Confine(argv []string) []string {
	if s.noConfine {
		return argv
	}
	exe := real(argv[0])
	if s.kind == "seatbelt" {
		return append([]string{"sandbox-exec", "-p", confineProfile(exe, s.home), exe}, argv[1:]...)
	}
	args := []string{"bwrap", "--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	// Hide personal and shared scratch files behind empty directories,
	// then bring back the program itself if it lives under one of them.
	for _, p := range hiddenDirs(s.home) {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			args = append(args, "--tmpfs", p)
		}
	}
	args = append(args, "--ro-bind", exe, exe, "--chdir", "/", "--")
	return append(append(args, exe), argv[1:]...)
}

// hiddenDirs are the directories a confined program can't read.
func hiddenDirs(home string) []string {
	dirs := []string{"/home", "/root", "/tmp", "/var/tmp", "/run/user", "/media", "/mnt"}
	if home != "" && home != "/" && !strings.HasPrefix(home+"/", "/home/") {
		dirs = append(dirs, home)
	}
	return dirs
}

// confineProfile is the Seatbelt profile for Confine. The Go runtime
// needs sysctl-read (it asks for the page size before anything else).
func confineProfile(exe, home string) string {
	denied := []string{"/Users", "/private/tmp", "/private/var/folders", "/Volumes"}
	if home != "" && home != "/" {
		denied = append(denied, home)
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	b.WriteString("(allow process-exec (literal " + sbQuote(exe) + "))\n")
	b.WriteString("(allow sysctl-read)\n(allow file-read*)\n")
	// Later rules win: no personal files, except the program itself.
	b.WriteString("(deny file-read*")
	for _, p := range denied {
		b.WriteString("\n  (subpath " + sbQuote(real(p)) + ")")
	}
	b.WriteString(")\n(allow file-read* (literal " + sbQuote(exe) + "))\n")
	return b.String()
}

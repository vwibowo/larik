package sandbox

import (
	"errors"
	"os/exec"
	"strings"
	"time"
)

// probeTimeout bounds a startup probe, which should take milliseconds.
const probeTimeout = 10 * time.Second

// probe runs a trivial command the way bash commands run, and returns why
// it failed, if it did.
func (s *Sandbox) probe() error {
	cmd := s.Command("true", s.root)
	err := runProbe(cmd)
	s.Finished(cmd, false)
	return err
}

// probeConfine does the same for Confine's stricter sandbox.
func (s *Sandbox) probeConfine() error {
	t, err := exec.LookPath("true")
	if err != nil {
		return nil // nothing to probe with; Confine is tried as it is
	}
	argv := s.Confine([]string{t})
	return runProbe(exec.Command(argv[0], argv[1:]...))
}

func runProbe(cmd *exec.Cmd) error {
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return err
	}
	timer := time.AfterFunc(probeTimeout, func() { cmd.Process.Kill() })
	defer timer.Stop()
	if err := cmd.Wait(); err != nil {
		if msg := firstLine(strings.TrimSpace(out.String())); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// probeHint suggests a fix for the usual reasons a sandbox can't start.
func probeHint(kind, msg string) string {
	switch {
	case kind == "bubblewrap" && strings.Contains(msg, "mount proc"):
		return ". Inside a container, start it with --security-opt systempaths=unconfined (plus seccomp=unconfined and apparmor=unconfined) so the sandbox can mount /proc"
	case kind == "bubblewrap" && (strings.Contains(msg, "uid map") || strings.Contains(msg, "namespace")):
		return ". Unprivileged user namespaces look disabled: on Ubuntu 24.04+ AppArmor restricts them (sysctl kernel.apparmor_restrict_unprivileged_userns); elsewhere check kernel.unprivileged_userns_clone or user.max_user_namespaces"
	case kind == "seatbelt" && strings.Contains(msg, "sandbox_apply"):
		return ". Larik seems to be running inside another sandbox, which macOS doesn't allow to nest"
	}
	return ""
}

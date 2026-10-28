package schedule

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"larik/internal/procgroup"
)

// tick is how often the daemon looks for due entries.
const tick = 20 * time.Second

// Daemon starts due entries until ctx ends. Each run is exe -p with the
// entry's prompt and flags, in its directory, with its output in a log
// under logDir. A run still going when its next time comes is not started
// again, and a time missed while no daemon ran is made up once.
type Daemon struct {
	Store  Store
	Exe    string // the larik binary
	LogDir string
	Logf   func(format string, args ...any)
	Now    func() time.Time // for tests; time.Now when nil

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// Run checks for due entries every tick until ctx ends, then waits for
// the runs it started, which ctx's end stops.
func (d *Daemon) Run(ctx context.Context) error {
	d.running = map[string]bool{}
	defer d.wg.Wait()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := d.startDue(ctx); err != nil {
			d.logf("schedule: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (d *Daemon) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Daemon) startDue(ctx context.Context) error {
	entries, err := d.Store.List()
	if err != nil {
		return err
	}
	now := d.now()
	for _, e := range entries {
		next := e.Next()
		if next.IsZero() || next.After(now) {
			continue
		}
		d.mu.Lock()
		busy := d.running[e.ID]
		if !busy {
			d.running[e.ID] = true
		}
		d.mu.Unlock()
		if busy {
			continue
		}
		// Recorded at once, so a crash mid-run doesn't repeat it and the
		// next time is counted from now.
		log := filepath.Join(d.LogDir, e.ID, now.Format("20060102-150405")+".log")
		if err := d.Store.record(e.ID, now, "running", log); err != nil {
			d.done(e.ID)
			return err
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer d.done(e.ID)
			status := d.runOne(ctx, e, log)
			if err := d.Store.record(e.ID, now, status, log); err != nil {
				d.logf("schedule %s: %v", e.ID, err)
			}
			d.logf("schedule %s: %s (log %s)", e.ID, status, log)
		}()
	}
	return nil
}

func (d *Daemon) done(id string) {
	d.mu.Lock()
	delete(d.running, id)
	d.mu.Unlock()
}

// Args are the larik flags that run e headlessly.
func Args(e Entry) []string {
	args := []string{"-p"}
	if e.Mode != "" {
		args = append(args, "--mode", e.Mode)
	}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	if e.Timeout != "" {
		args = append(args, "--timeout", e.Timeout)
	}
	return append(args, "--", e.Prompt)
}

func (d *Daemon) runOne(ctx context.Context, e Entry, log string) string {
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		return err.Error()
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	d.logf("schedule %s: starting in %s", e.ID, e.Cwd)
	cmd := exec.Command(d.Exe, Args(e)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = e.Cwd, f, f
	procgroup.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = procgroup.Kill(cmd)
		<-done
		return "stopped with the daemon"
	}
	if err != nil {
		return fmt.Sprintf("failed: %v", err)
	}
	return "ok"
}

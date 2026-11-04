package main

import (
	"io"
	"os"

	"larik/internal/config"
	"larik/internal/desktop"
	"larik/internal/status"
)

// runDesktopHook is invoked by installed lifecycle hooks. It never changes
// the agent's decision or fails its turn when Desktop is unavailable.
func runDesktopHook(args []string, stdin io.Reader) error {
	if len(args) != 2 || os.Getenv(status.TabEnv) == "" {
		return nil
	}
	input, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return nil
	}
	req, ok := desktop.HookRequest(args[0], args[1], os.Getenv(status.TabEnv), input)
	if ok {
		_, _ = desktop.Call(config.DataDir(), req)
	}
	return nil
}

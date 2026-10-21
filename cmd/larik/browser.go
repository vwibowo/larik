package main

import (
	"errors"
	"flag"
	"fmt"

	"larik/internal/termbrowser"
)

// runBrowser is "larik browser view --socket <path>": the viewer a
// browser_show call opens in a pane beside Larik's.
func runBrowser(args []string) error {
	usage := errors.New("usage: larik browser view --socket <path>")
	if len(args) == 0 || args[0] != "view" {
		return usage
	}
	fs := flag.NewFlagSet("larik browser view", flag.ContinueOnError)
	socket := fs.String("socket", "", "the control socket of the Larik session to show")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *socket == "" {
		return usage
	}
	if err := termbrowser.RunViewer(*socket); err != nil {
		return fmt.Errorf("browser view: %w", err)
	}
	return nil
}

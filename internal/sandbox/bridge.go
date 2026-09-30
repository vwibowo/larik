package sandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// BridgeCommand is the hidden larik subcommand that runs first inside a
// Linux sandbox with a network allowlist. bubblewrap's network namespace
// has only its own loopback, so the proxy on the host's loopback is out of
// reach; the bridge listens on the proxy's port inside the namespace and
// forwards each connection to the proxy's Unix socket, which is in the
// sandbox's temp directory and so visible inside. Then it runs the command.
const BridgeCommand = "__sandbox-bridge"

// BridgeMain runs the bridge: args are the socket, the port, "--" and the
// command. It returns the command's exit code.
func BridgeMain(args []string) int {
	if len(args) < 4 || args[2] != "--" {
		fmt.Fprintln(os.Stderr, "usage: larik "+BridgeCommand+" SOCKET PORT -- COMMAND...")
		return 2
	}
	sock, port, command := args[0], args[1], args[3:]
	if ln, err := net.Listen("tcp", "127.0.0.1:"+port); err != nil {
		fmt.Fprintf(os.Stderr, "larik sandbox: the network bridge couldn't start (%v); the network is unreachable\n", err)
	} else {
		go bridge(ln, sock)
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "larik sandbox: %v\n", err)
		return 127
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			_ = cmd.Process.Signal(sig)
		}
	}()
	err := cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		return 128 + 9 // killed by a signal
	default:
		return 1
	}
}

// bridge forwards each connection ln accepts to the Unix socket.
func bridge(ln net.Listener, sock string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			u, err := net.Dial("unix", sock)
			if err != nil {
				c.Close()
				return
			}
			pipe(c, u)
		}()
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/benenen/ah/internal/cli"
	"golang.org/x/crypto/ssh"
)

func main() { os.Exit(run()) }
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ah:", err)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return 130
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			if code := exit.ExitStatus(); code > 0 && code < 256 {
				return code
			}
		}
		return 1
	}
	return 0
}

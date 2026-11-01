package cli

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/benenen/ah/internal/forward"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
)

func retryForward(ctx context.Context, attempt func() error) error {
	return retryForwardWithDelay(ctx, time.Second, 30*time.Second, attempt)
}

func retryForwardWithDelay(ctx context.Context, delay, maximum time.Duration, attempt func() error) error {
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := attempt(); err == nil {
			return nil
		}
		if delay < maximum {
			delay *= 2
			if delay > maximum {
				delay = maximum
			}
		}
	}
}

// Initial startup still fails immediately. Reconnection uses current config
// without prompts and always verifies host keys, including on remote forwards.
func (a *app) reopenForward(cmd *cobra.Command, record forward.Record) (*sshclient.Client, net.Listener, error) {
	c, err := a.connection(record.Name)
	if err != nil {
		return nil, nil, err
	}
	client, err := a.dialSSH(cmd.Context(), cmd, record.Name, c, a.connectionSSHOptions(cmd, record.Name, c, false), false)
	if err != nil {
		return nil, nil, err
	}
	var listener net.Listener
	if record.Kind() == forward.Remote {
		listener, err = client.ListenRemote(cmd.Context(), record.Listen)
	} else {
		listener, err = (&net.ListenConfig{}).Listen(cmd.Context(), "tcp", record.Listen)
	}
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("reopen forwarding listener: %w", err)
	}
	return client, listener, nil
}

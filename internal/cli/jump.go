package cli

import (
	"context"
	"fmt"

	"github.com/benenen/ah/internal/config"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
)

func (a *app) dialSSH(ctx context.Context, cmd *cobra.Command, name string, c config.Connection, opts sshclient.Options, interactive bool) (*sshclient.Client, error) {
	hops, err := a.resolveSSHRoute(name, c)
	if err != nil {
		return nil, err
	}
	var parent *sshclient.Client
	for i := len(hops) - 1; i >= 0; i-- {
		h := hops[i]
		current := opts
		if i > 0 {
			current = a.connectionSSHOptions(cmd, h.name, h.c, interactive)
			current.Timeout = opts.Timeout
			if opts.Observe != nil {
				current.Observe = func(stage string, err error) { opts.Observe("jump-"+h.name+"/"+stage, err) }
			}
		}
		var client *sshclient.Client
		var err error
		if parent == nil {
			client, err = sshclient.Dial(ctx, h.c, current)
		} else {
			client, err = sshclient.DialVia(ctx, h.c, current, parent)
		}
		if err != nil {
			return nil, fmt.Errorf("SSH connection %s: %w", h.name, err)
		}
		parent = client
	}
	return parent, nil
}

type sshHop struct {
	name string
	c    config.Connection
}

func (a *app) resolveSSHRoute(name string, c config.Connection) ([]sshHop, error) {
	// Resolve the complete route before opening sockets or prompting.
	hops := []sshHop{{name, c}}
	seen := map[string]bool{name: true}
	for c.Jump != "" {
		name = c.Jump
		if seen[name] {
			return nil, fmt.Errorf("SSH jump cycle at %q", name)
		}
		if len(hops) >= 32 {
			return nil, fmt.Errorf("SSH jump chain exceeds 32 connections")
		}
		seen[name] = true
		var err error
		c, err = a.connection(name)
		if err != nil {
			return nil, fmt.Errorf("jump: %w", err)
		}
		hops = append(hops, sshHop{name, c})
	}
	return hops, nil
}

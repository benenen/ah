package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/benenen/ah/internal/forward"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
)

func (a *app) forwardCommand() *cobra.Command {
	var daemon, local, dynamic, remote bool
	var worker string
	cmd := &cobra.Command{
		Use:     "forward NAME LISTEN_PORT_OR_ADDRESS [TARGET_PORT_OR_ADDRESS] [-L | -D | -R]",
		Short:   "Forward TCP ports or run a SOCKS5 proxy through SSH",
		Aliases: []string{"f"},
		Long: `通过 SSH 转发 TCP 端口。

本地转发（-L，默认，类似 ssh -L）：ah forward NAME 本地监听端口 SSH服务器侧目标端口
  NAME                     已保存的 SSH 连接名
  LOCAL_PORT_OR_ADDRESS    本地监听端口或地址，例如 8080、0.0.0.0:8080
  TARGET_PORT_OR_ADDRESS   SSH 服务器侧的目标端口或地址，例如 80、database.internal:5432
例如 8080 80 表示：本地 127.0.0.1:8080 → SSH 服务器上的 127.0.0.1:80。
目标端口是要访问的服务端口；SSH 登录端口使用连接配置中的 port。
显式指定目标主机时，由 SSH 服务器访问该主机。

动态转发（-D，类似 ssh -D）：ah forward NAME 本地监听端口 -D
在本地起一个 SOCKS5 代理（无认证，仅 CONNECT），每个请求由 SSH 服务器连接目标，
域名也由 SSH 服务器解析；浏览器需开启“通过 SOCKS 代理 DNS”（socks5h）才能访问内网域名。
单个目标连不上只断开该请求，不结束代理。

远程转发（-R，类似 ssh -R）：ah forward NAME SSH服务器侧监听端口 本地目标端口
让 SSH 服务器监听端口，连接转到本机（运行 ah 的机器）的目标。
例如 9000 3000 -R 表示：SSH 服务器上的 127.0.0.1:9000 → 本机 127.0.0.1:3000。
服务器侧监听非回环地址需要 sshd 开启 GatewayPorts，否则 sshd 仍只监听回环地址。

地址省略 IP 时均默认 127.0.0.1。`,
		Example: "  ah forward myserver 8080 80 -d\n  ah forward myserver 0.0.0.0:8080 80 -d\n  ah forward myserver 1080 -D -d\n  ah forward myserver 9000 3000 -R -d",
		Args: func(cmd *cobra.Command, args []string) error {
			if dynamic {
				return cobra.ExactArgs(2)(cmd, args)
			}
			return cobra.ExactArgs(3)(cmd, args)
		},
		ValidArgsFunction: a.completeNames,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			var ready *os.File
			if worker != "" {
				ready = os.NewFile(3, "forward-ready")
				info, statErr := ready.Stat()
				if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
					return fmt.Errorf("forward worker requires a startup pipe")
				}
				defer func() { _ = ready.Close() }()
				defer func() {
					if err != nil {
						_ = json.NewEncoder(ready).Encode(startupResult{Error: err.Error()})
					}
				}()
			}
			kind, listen, target := forward.Local, "", ""
			switch {
			case dynamic:
				kind = forward.Dynamic
				listen, err = parseForwardAddress(args[1])
			case remote:
				kind = forward.Remote
				listen, target, err = parseLocalForward(args[1], args[2])
			default:
				listen, target, err = parseLocalForward(args[1], args[2])
			}
			if err != nil {
				return err
			}
			if _, err = a.connection(args[0]); err != nil {
				return err
			}
			var record forward.Record
			if worker == "" {
				record, err = forward.Allocate(kind, args[0], listen, target)
			} else {
				record, err = forward.Read(worker)
				if err == nil && (record.Status != "starting" || record.Kind() != kind || record.Name != args[0] || record.Listen != listen || record.Target != target) {
					err = fmt.Errorf("forward worker does not match its starting record")
				}
			}
			if err != nil {
				return err
			}
			var unlock func() error
			if worker == "" {
				lock, lockErr := forward.Lock(cmd.Context(), record.ID)
				if lockErr != nil {
					return lockErr
				}
				unlock = lock.Unlock
				defer func() { err = errors.Join(err, lock.Close()) }()
				// Re-read after acquiring the lifecycle lock.
				record, err = forward.Read(record.ID)
				if err != nil {
					return err
				}
				if err = a.captureForwardOptions(&record); err != nil {
					return err
				}
				if err = record.Save(); err != nil {
					return err
				}
			}
			handedOff := false
			defer func() {
				if handedOff {
					return
				}
				if current, readErr := forward.Read(record.ID); err != nil && readErr == nil && current.Status == "starting" {
					record.Status, record.Error = "failed", err.Error()
					err = errors.Join(err, record.Save())
				}
			}()
			if daemon && worker == "" {
				if err = a.startForwardDaemon(cmd, record); err != nil {
					return err
				}
				handedOff = true
				_, err = fmt.Fprintln(cmd.OutOrStdout(), record.ID)
				return err
			}
			handedOff = true
			return a.runForward(cmd, record, ready, unlock)
		},
	}
	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "run in the background and print the generated ID")
	cmd.Flags().BoolVarP(&dynamic, "dynamic", "D", false, "run a local SOCKS5 proxy whose connections the SSH server makes")
	cmd.Flags().BoolVarP(&remote, "remote", "R", false, "listen on the SSH server and forward connections to a local target")
	cmd.Flags().BoolVarP(&local, "local", "L", false, "listen locally and forward connections to a target the SSH server dials (default)")
	cmd.MarkFlagsMutuallyExclusive("local", "dynamic", "remote")
	cmd.Flags().StringVar(&worker, "forward-worker", "", "internal worker ID")
	_ = cmd.Flags().MarkHidden("forward-worker")
	var jsonOutput bool
	ls := &cobra.Command{
		Use: "ls", Short: "List forwards and their status", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			records, err := forward.List(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeForwardJSON(cmd.OutOrStdout(), records)
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(out, "ID\tTYPE\tNAME\tLISTEN\tTARGET\tPID\tSTATUS\tERROR"); err != nil {
				return err
			}
			for _, r := range records {
				kind, target := forwardFlags[r.Kind()], r.Target
				if r.Status == "corrupt" {
					kind = ""
				}
				if r.Kind() == forward.Dynamic {
					target = "(socks5)"
				}
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.ID, kind, r.Name, r.Listen, target, r.PID, r.Status, r.Error); err != nil {
					return err
				}
			}
			return out.Flush()
		},
	}
	ls.Flags().BoolVar(&jsonOutput, "json", false, "print records as JSON")
	cmd.AddCommand(ls, &cobra.Command{
		Use: "kill ID", Short: "Stop a forward by ID", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout+5*time.Second)
			defer cancel()
			// The launcher holds the lock while connecting or prompting for trust.
			// Cancel startup first so it can finish and release that lock.
			stoppedStartup := false
			current, err := forward.Read(args[0])
			if err != nil {
				return err
			}
			if current.Status == "starting" {
				if err := forward.Stop(ctx, current.ID); err != nil {
					return err
				}
				stoppedStartup = true
			}
			lock, err := forward.Lock(ctx, args[0])
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			current, err = forward.Read(args[0])
			if err != nil {
				return err
			}
			if !stoppedStartup || (current.Status != "stopped" && current.Status != "failed") {
				if err := forward.Stop(ctx, args[0]); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", args[0])
			return err
		},
	})
	cmd.AddCommand(a.forwardRemoveCommand(), a.forwardStartCommand(false), a.forwardStartCommand(true))
	return cmd
}

func (a *app) runForward(cmd *cobra.Command, record forward.Record, ready *os.File, unlock func() error) (err error) {
	var reg *forward.Registration
	defer func() {
		if reg == nil && err != nil {
			record.Status, record.Error = "failed", err.Error()
			err = errors.Join(err, record.Save())
		}
	}()
	connection, err := a.connection(record.Name)
	if err != nil {
		return err
	}
	parentCtx := cmd.Context()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	cmd.SetContext(ctx)
	defer cmd.SetContext(parentCtx)
	reg, err = forward.RegisterStarting(record, cancel)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reg.Close(err)) }()
	kind := record.Kind()
	var listener net.Listener
	// Bind a local port before connecting so a busy port fails without SSH.
	if kind != forward.Remote {
		listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", record.Listen)
		if err != nil {
			return fmt.Errorf("listen for forwarding: %w", err)
		}
		defer func() { _ = listener.Close() }()
	}
	client, err := sshclient.Dial(ctx, connection, a.connectionSSHOptions(cmd, record.Name, connection, true))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if kind == forward.Remote {
		listener, err = client.ListenRemote(ctx, record.Listen)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
	}
	record.Listen = listener.Addr().String()
	reg.Record.Listen = record.Listen
	if err = reg.Running(); err != nil {
		return err
	}
	if ready != nil {
		if err = json.NewEncoder(ready).Encode(startupResult{ID: record.ID}); err != nil {
			return err
		}
		if err = ready.Close(); err != nil {
			return err
		}
	} else {
		if _, err = fmt.Fprintln(cmd.OutOrStdout(), record.ID); err != nil {
			return err
		}
		var banner string
		switch kind {
		case forward.Dynamic:
			banner = fmt.Sprintf("SOCKS5 proxy on %s via %s", record.Listen, record.Name)
		case forward.Remote:
			banner = fmt.Sprintf("Forwarding %s on %s -> local %s", record.Listen, record.Name, record.Target)
		default:
			banner = fmt.Sprintf("Forwarding %s -> %s via %s", record.Listen, record.Target, record.Name)
		}
		if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "%s (Ctrl+C to stop)\n", banner); err != nil {
			return err
		}
	}
	if unlock != nil {
		if err = unlock(); err != nil {
			return err
		}
	}
	switch kind {
	case forward.Dynamic:
		// Per-request failures go to stderr, which is the log file for a daemon.
		var mu sync.Mutex
		stderr := cmd.ErrOrStderr()
		err = client.ForwardDynamic(ctx, listener, func(requestErr error) {
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(stderr, "%s %v\n", time.Now().Format(time.RFC3339), requestErr)
		})
	case forward.Remote:
		err = client.ForwardRemote(ctx, listener, record.Target)
	default:
		err = client.ForwardLocal(ctx, listener, record.Target)
	}
	if errors.Is(err, context.Canceled) && parentCtx.Err() == nil {
		return nil // A control-channel stop is a normal shutdown.
	}
	return err
}

// forwardFlags labels each forward type in `ls` with the flag that creates it.
var forwardFlags = map[string]string{forward.Local: "-L", forward.Dynamic: "-D", forward.Remote: "-R"}

type startupResult struct{ ID, Error string }

func parseLocalForward(local, target string) (string, string, error) {
	local, err := parseForwardAddress(local)
	if err != nil {
		return "", "", err
	}
	target, err = parseForwardAddress(target)
	if err != nil {
		return "", "", err
	}
	return local, target, nil
}

// parseForwardAddress accepts PORT or HOST:PORT; a bare port means 127.0.0.1.
func parseForwardAddress(address string) (string, error) {
	if !strings.Contains(address, ":") {
		address = net.JoinHostPort("127.0.0.1", address)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid forwarding address %q: %w", address, err)
	}
	if host == "" || strings.ContainsAny(host, "[] \t\r\n") {
		return "", fmt.Errorf("forwarding hosts must not be empty or contain whitespace")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", fmt.Errorf("forwarding ports must be between 1 and 65535")
	}
	return address, nil
}

// forwardJSON is the stable shape of `ah forward ls --json`. Internal record
// fields such as the transient control socket stay out of the CLI contract.
type forwardJSON struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Name    string    `json:"name"`
	Listen  string    `json:"listen"`
	Target  string    `json:"target"`
	PID     int       `json:"pid"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Error   string    `json:"error,omitempty"`
}

func writeForwardJSON(w io.Writer, records []forward.Record) error {
	out := make([]forwardJSON, 0, len(records))
	for _, r := range records {
		out = append(out, forwardJSON{
			ID: r.ID, Type: r.Kind(), Name: r.Name, Listen: r.Listen, Target: r.Target,
			PID: r.PID, Status: r.Status, Started: r.Started, Error: r.Error,
		})
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

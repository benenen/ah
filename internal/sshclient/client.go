// Package sshclient owns verified SSH connections and their cancellation lifecycle.
package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benenen/ah/internal/config"
	"github.com/gofrs/flock"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Options struct {
	SudoPassword func(context.Context) ([]byte, error)
	PasswordOnly bool
	KnownHosts   string
	Timeout      time.Duration
	// Callbacks run synchronously and must arrange their own cancellation.
	Password   func() (string, error)
	Passphrase func(string) ([]byte, error)
	TrustHost  func(string, string) (bool, error)
}

type Client struct {
	ctx          context.Context
	timeout      time.Duration
	sudo         bool
	sftpServer   string
	sudoShell    string
	sudoPassword func(context.Context) ([]byte, error)
	client       *ssh.Client
	conn         net.Conn
	stop         func() bool
	once         sync.Once
	closeErr     error
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func Dial(ctx context.Context, c config.Connection, opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if c.Port == 0 {
		c.Port = 22
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SSH connection: %w", err)
	}
	if opts.KnownHosts == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		opts.KnownHosts = filepath.Join(home, ".ssh", "known_hosts")
	}
	knownPath, err := expandHome(opts.KnownHosts)
	if err != nil {
		return nil, err
	}
	opts.KnownHosts = knownPath
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, opts.Timeout)
	defer cancelHandshake()
	auth, agentConn, err := authentication(handshakeCtx, c, opts)
	if err != nil {
		return nil, err
	}
	if agentConn != nil {
		defer agentConn.Close()
		agentStop := context.AfterFunc(handshakeCtx, func() { agentConn.Close() })
		defer agentStop()
	}
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := dialTransport(handshakeCtx, address, c.ProxyChain())
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", address, err)
	}
	stopHandshake := context.AfterFunc(handshakeCtx, func() { conn.Close() })
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopHandshake()
	deadline := time.Now().Add(opts.Timeout)
	if d, ok := handshakeCtx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	cfg := &ssh.ClientConfig{User: c.User, Auth: auth, HostKeyCallback: hostKeyCallback(handshakeCtx, opts)}
	sc, ch, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		stop()
		conn.Close()
		if handshakeCtx.Err() != nil {
			return nil, handshakeCtx.Err()
		}
		return nil, fmt.Errorf("SSH handshake %s: %w", address, err)
	}
	if !stopHandshake() {
		stop()
		sc.Close()
		return nil, handshakeCtx.Err()
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		stop()
		sc.Close()
		return nil, err
	}
	return &Client{client: ssh.NewClient(sc, ch, reqs), conn: conn, stop: stop, ctx: ctx, timeout: opts.Timeout, sudo: c.Sudo, sftpServer: c.SFTPServer, sudoShell: c.SudoShell, sudoPassword: opts.SudoPassword}, nil
}

func authentication(ctx context.Context, c config.Connection, opts Options) ([]ssh.AuthMethod, net.Conn, error) {
	if opts.PasswordOnly {
		if opts.Password == nil {
			return nil, nil, fmt.Errorf("password authentication requires a password")
		}
		return []ssh.AuthMethod{ssh.PasswordCallback(opts.Password)}, nil, nil
	}
	var methods []ssh.AuthMethod
	var allSigners []ssh.Signer
	var agentSigners []ssh.Signer
	var agentConn net.Conn
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		var err error
		agentConn, err = (&net.Dialer{Timeout: opts.Timeout}).DialContext(ctx, "unix", sock)
		if err == nil {
			agentConn.SetDeadline(time.Now().Add(opts.Timeout))
			socket := agentConn
			stop := context.AfterFunc(ctx, func() { socket.Close() })
			signers, e := agent.NewClient(agentConn).Signers()
			stop()
			if e == nil {
				agentSigners = signers
			} else {
				agentConn.Close()
				agentConn = nil
			}
		}
	}
	paths := []string{c.IdentityFile}
	explicit := c.IdentityFile != ""
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			if agentConn != nil {
				agentConn.Close()
			}
			return nil, nil, err
		}
		paths = []string{filepath.Join(home, ".ssh", "id_ed25519"), filepath.Join(home, ".ssh", "id_rsa")}
	}
	for _, path := range paths {
		path, err := expandHome(path)
		if err != nil {
			if agentConn != nil {
				agentConn.Close()
			}
			return nil, nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if !explicit && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if agentConn != nil {
				agentConn.Close()
			}
			return nil, nil, fmt.Errorf("read identity %s: %w", path, err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		var encrypted *ssh.PassphraseMissingError
		if errors.As(err, &encrypted) && encrypted.PublicKey != nil {
			for _, candidate := range agentSigners {
				if string(candidate.PublicKey().Marshal()) == string(encrypted.PublicKey.Marshal()) {
					signer = candidate
					err = nil
					break
				}
			}
		}
		if err != nil && errors.As(err, &encrypted) && opts.Passphrase != nil {
			pass, e := opts.Passphrase(path)
			if e != nil {
				err = e
			} else {
				signer, err = ssh.ParsePrivateKeyWithPassphrase(data, pass)
				clear(pass)
			}
		}
		if err != nil {
			if !explicit && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if agentConn != nil {
				agentConn.Close()
			}
			return nil, nil, fmt.Errorf("parse identity %s: %w", path, err)
		}
		allSigners = append(allSigners, signer)
	}
	if !explicit {
		allSigners = append(allSigners, agentSigners...)
	}
	if len(allSigners) > 0 {
		methods = append(methods, ssh.PublicKeys(allSigners...))
	}
	if opts.Password != nil {
		methods = append(methods, ssh.PasswordCallback(opts.Password))
	}
	return methods, agentConn, nil
}

func hostKeyCallback(ctx context.Context, opts Options) ssh.HostKeyCallback {
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		verify := func() error {
			callback, err := knownhosts.New(opts.KnownHosts)
			if errors.Is(err, os.ErrNotExist) {
				return &knownhosts.KeyError{}
			}
			if err != nil {
				return err
			}
			return callback(host, remote, key)
		}
		err := verify()
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
			return fmt.Errorf("host key verification failed for %s: %w", host, err)
		}
		if opts.TrustHost == nil {
			return fmt.Errorf("unknown host %s (%s): %w", host, ssh.FingerprintSHA256(key), err)
		}
		trust, err := opts.TrustHost(host, ssh.FingerprintSHA256(key))
		if err != nil {
			return err
		}
		if !trust {
			return fmt.Errorf("host %s was not trusted", host)
		}
		if err = os.MkdirAll(filepath.Dir(opts.KnownHosts), 0700); err != nil {
			return err
		}
		lock := flock.New(opts.KnownHosts + ".lock")
		locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
		if err != nil {
			return err
		}
		if !locked {
			return fmt.Errorf("known_hosts lock: %w", ctx.Err())
		}
		defer lock.Unlock()
		// Recheck under the lock: another process may have saved a different key.
		err = verify()
		if err == nil {
			return nil
		}
		if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
			return fmt.Errorf("host key changed while trusting %s: %w", host, err)
		}
		file, err := os.OpenFile(opts.KnownHosts, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if err = file.Chmod(0600); err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		prefix := ""
		if info.Size() > 0 {
			last := []byte{0}
			if _, err = file.ReadAt(last, info.Size()-1); err != nil {
				return err
			}
			if last[0] != '\n' {
				prefix = "\n"
			}
		}
		if _, err = file.WriteString(prefix + knownhosts.Line([]string{host}, key) + "\n"); err != nil {
			return err
		}
		return file.Sync()
	}
}

func (c *Client) SFTP() (*sftp.Client, error) {
	if c.sudo {
		return c.sudoSFTP()
	}
	return sftp.NewClient(c.client)
}
func (c *Client) Close() error {
	c.once.Do(func() { c.stop(); c.closeErr = c.client.Close() })
	return c.closeErr
}

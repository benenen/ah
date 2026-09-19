// Package forward tracks local forwarding processes and their private control sockets.
package forward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Record struct {
	ID      string
	Name    string
	Listen  string
	Target  string
	PID     int
	Status  string
	Started time.Time
	Error   string `json:",omitempty"`
	Socket  string `json:",omitempty"`
}

func directory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ah", "forwards"), nil
}

func recordPath(id string) (string, error) {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 8 {
		return "", fmt.Errorf("invalid forward ID %q", id)
	}
	dir, err := directory()
	return filepath.Join(dir, id+".json"), err
}

// Allocate reserves a unique ID before a process is started.
func Allocate(name, listen, target string) (Record, error) {
	dir, err := directory()
	if err != nil {
		return Record{}, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Record{}, err
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Record{}, err
	}
	r := Record{ID: hex.EncodeToString(random[:]), Name: name, Listen: listen, Target: target, Status: "starting", Started: time.Now()}
	p, _ := recordPath(r.ID)

	f, err := os.CreateTemp(dir, ".forward-*")
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(r)
	if err = errors.Join(err, f.Close()); err != nil {
		return Record{}, err
	}
	// Publish complete JSON without overwriting an existing ID.
	return r, os.Link(f.Name(), p)
}

func Read(id string) (Record, error) {
	p, err := recordPath(id)
	if err != nil {
		return Record{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.ID != id {
		return r, fmt.Errorf("forward record ID mismatch")
	}
	return r, nil
}

func (r Record) Save() error {
	p, err := recordPath(r.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".forward-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

type Registration struct {
	Record    Record
	listener  net.Listener
	socketDir string
	done      chan struct{}
}

// Register publishes a running tunnel only after its SSH and TCP listeners exist.
func Register(r Record, cancel context.CancelFunc) (*Registration, error) {
	socketDir, err := os.MkdirTemp("", "ah-forward-")
	if err != nil {
		return nil, err
	}
	r.Socket = filepath.Join(socketDir, "control.sock")
	listener, err := net.Listen("unix", r.Socket)
	if err != nil {
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	r.Status, r.PID = "running", os.Getpid()
	if err = r.Save(); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(socketDir)
		return nil, err
	}
	reg := &Registration{Record: r, listener: listener, socketDir: socketDir, done: make(chan struct{})}
	go func() {
		defer close(reg.done)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
				_ = c.Close()
				continue
			}
			var request struct{ ID, Action string }
			err = json.NewDecoder(io.LimitReader(c, 1024)).Decode(&request)
			if err == nil && request.ID == r.ID && (request.Action == "ping" || request.Action == "stop") {
				err = json.NewEncoder(c).Encode(struct{ ID string }{r.ID})
				if err == nil && request.Action == "stop" {
					cancel()
				}
			}
			_ = c.Close()
		}
	}()
	return reg, nil
}

func (r *Registration) Close(runErr error) error {
	_ = r.listener.Close()
	<-r.done
	cleanupErr := os.RemoveAll(r.socketDir)
	r.Record.Socket = ""
	r.Record.Status = "stopped"
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		r.Record.Status, r.Record.Error = "failed", runErr.Error()
	}
	return errors.Join(cleanupErr, r.Record.Save())
}

func control(ctx context.Context, r Record, action string) error {
	if r.Status != "running" || r.Socket == "" {
		return fmt.Errorf("forward %s is %s", r.ID, r.Status)
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", r.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	if err = json.NewEncoder(c).Encode(struct{ ID, Action string }{r.ID, action}); err != nil {
		return err
	}
	var response struct{ ID string }
	if err = json.NewDecoder(io.LimitReader(c, 1024)).Decode(&response); err != nil {
		return err
	}
	if response.ID != r.ID {
		return fmt.Errorf("forward control ID mismatch")
	}
	return nil
}

func List(ctx context.Context) ([]Record, error) {
	dir, err := directory()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		r, err := Read(strings.TrimSuffix(f.Name(), ".json"))
		if err != nil {
			return nil, fmt.Errorf("read forward %s: %w", f.Name(), err)
		}
		if r.Status == "running" && control(ctx, r, "ping") != nil {
			r.Status = "stale"
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Started.Before(records[j].Started) })
	return records, nil
}

func Stop(ctx context.Context, id string) error {
	r, err := Read(id)
	if err != nil {
		return err
	}
	if err = control(ctx, r, "stop"); err != nil {
		return fmt.Errorf("stop forward %s: %w", id, err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		r, err = Read(id)
		if err != nil {
			return err
		}
		if r.Status != "running" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

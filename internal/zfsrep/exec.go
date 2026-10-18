package zfsrep

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
)

// Exec runs the commands on this host.
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, commandError(err, &stderr)
	}
	return out, nil
}

func (Exec) Pipe(ctx context.Context, send, recv []string) (uint64, error) {
	var sendErr, recvErr bytes.Buffer
	s := exec.CommandContext(ctx, send[0], send[1:]...)
	r := exec.CommandContext(ctx, recv[0], recv[1:]...)
	s.Stderr, r.Stderr = &sendErr, &recvErr
	out, err := s.StdoutPipe()
	if err != nil {
		return 0, err
	}
	in, err := r.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err := r.Start(); err != nil {
		return 0, err
	}
	if err := s.Start(); err != nil {
		in.Close()
		_ = r.Wait()
		return 0, err
	}
	var n atomic.Uint64
	_, copyErr := io.Copy(in, io.TeeReader(out, counter{&n}))
	in.Close()
	se, re := s.Wait(), r.Wait()
	switch {
	case se != nil:
		return n.Load(), fmt.Errorf("zfs send: %w", commandError(se, &sendErr))
	case re != nil:
		return n.Load(), fmt.Errorf("zfs receive: %w", commandError(re, &recvErr))
	case copyErr != nil:
		return n.Load(), copyErr
	}
	return n.Load(), nil
}

type counter struct{ n *atomic.Uint64 }

func (c counter) Write(b []byte) (int, error) {
	c.n.Add(uint64(len(b)))
	return len(b), nil
}

func commandError(err error, stderr *bytes.Buffer) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return err
}

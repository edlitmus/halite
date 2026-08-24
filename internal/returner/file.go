package returner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func init() {
	register("local", true, func(opts Options) (Returner, error) {
		if opts.Path == "" {
			opts.Path = filepath.Join(opts.StateDir, "returns.ndjson")
		}
		return newFileReturner("local", opts)
	})
	register("file", true, func(opts Options) (Returner, error) {
		if opts.Path == "" {
			return nil, fmt.Errorf("the file returner needs a path")
		}
		return newFileReturner("file", opts)
	})
}

// fileReturner appends NDJSON, one object per line.
//
// `local` and `file` are the same mechanism with different defaults:
// SPEC 20.3 gives `local` a fixed place on the node and `file` a path
// and rotation. Sharing the implementation means the durability
// argument is made once.
//
// One object per line and append-only, so a write cut short by a full
// disk or a crash is a line that will not parse rather than a file that
// will not. Recovery is `grep -v` rather than a repair tool.
type fileReturner struct {
	name string
	opts Options

	mu   sync.Mutex
	file *os.File
	size int64
}

func newFileReturner(name string, opts Options) (*fileReturner, error) {
	if opts.Mode == 0 {
		// A return carries whatever the job printed, which is as
		// sensitive as the estate's most sensitive job.
		opts.Mode = 0o600
	}
	if opts.KeepFiles == 0 {
		opts.KeepFiles = 5
	}
	r := &fileReturner{name: name, opts: opts}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *fileReturner) Name() string { return r.name }

func (r *fileReturner) Return(ctx context.Context, ret *job.Return) error {
	raw, err := json.Marshal(ret)
	if err != nil {
		return err
	}
	return r.append(raw)
}

func (r *fileReturner) Event(ctx context.Context, e *eventbus.Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.append(raw)
}

func (r *fileReturner) append(raw []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.open(); err != nil {
		return err
	}
	line := append(raw, '\n')
	n, err := r.file.Write(line)
	r.size += int64(n)
	if err != nil {
		return err
	}
	return r.rotate()
}

func (r *fileReturner) open() error {
	if r.file != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Clean(r.opts.Path),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, os.FileMode(r.opts.Mode))
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.file, r.size = f, info.Size()
	return nil
}

// rotate moves the file aside once it is over the bound.
//
// Numbered suffixes rather than timestamps, because the question during
// an incident is "the one before this", and `.1` answers it without
// anyone having to know when the rotation happened.
func (r *fileReturner) rotate() error {
	if r.opts.MaxBytes <= 0 || r.size < r.opts.MaxBytes {
		return nil
	}
	if err := r.file.Close(); err != nil {
		return err
	}
	r.file = nil
	// Oldest first, so nothing is overwritten before it is moved.
	for i := r.opts.KeepFiles - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", r.opts.Path, i+1)
		newer := fmt.Sprintf("%s.%d", r.opts.Path, i)
		if _, err := os.Stat(newer); err != nil {
			continue
		}
		if err := os.Rename(newer, older); err != nil {
			return err
		}
	}
	if err := os.Rename(r.opts.Path, r.opts.Path+".1"); err != nil {
		return err
	}
	// The one past the last kept copy is removed rather than left to
	// accumulate under a name nothing rotates.
	_ = os.Remove(fmt.Sprintf("%s.%d", r.opts.Path, r.opts.KeepFiles+1))
	return r.open()
}

func (r *fileReturner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

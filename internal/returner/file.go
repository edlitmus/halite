package returner

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// File appends one JSON object per line to a local file. Results carry
// whatever the states reported, which can include file diffs, so the file
// is created 0600 — see docs/pillar-security.md on keeping confidential
// values out of results in the first place.
type File struct {
	path string

	mu   sync.Mutex
	file *os.File
}

// NewFile opens the log, creating its directory if needed.
func NewFile(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("returner file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("returner file: %w", err)
	}
	return &File{path: path, file: f}, nil
}

func (f *File) Name() string { return "file:" + f.path }

func (f *File) Return(rec Record) error {
	line, err := encode(rec)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return fmt.Errorf("%s is closed", f.Name())
	}
	if _, err := f.file.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", f.path, err)
	}
	// This file is the durable record of what ran; a write sitting in the
	// page cache when the host loses power is not durable. One record per
	// sync is cheap at the rate results arrive.
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", f.path, err)
	}
	return nil
}

func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

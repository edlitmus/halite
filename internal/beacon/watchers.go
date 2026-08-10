package beacon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/modules"
)

// Disk raises an event when a filesystem crosses a usage threshold, and
// again when it drops back under.
type Disk struct {
	Mount     string
	Threshold int
	Every     time.Duration

	used    func(path string) (int, error) // swapped in tests
	state   state
	errored bool
}

func (d *Disk) Name() string            { return "disk" }
func (d *Disk) Interval() time.Duration { return d.Every }

func (d *Disk) Check() []Emission {
	usedPercent, err := d.usage()
	if err != nil {
		// A mount that cannot be read is worth one event, not one a
		// minute — and it is its own condition, kept apart from the
		// threshold edge so a later real alert still fires with its data.
		if !d.errored {
			d.errored = true
			return []Emission{{Data: map[string]any{
				"mount": d.Mount, "error": err.Error(),
			}}}
		}
		return nil
	}
	d.errored = false
	over := usedPercent >= d.Threshold
	if !d.state.transition(over) {
		return nil
	}
	return []Emission{{Data: map[string]any{
		"mount":     d.Mount,
		"used":      usedPercent,
		"threshold": d.Threshold,
		"over":      over,
	}}}
}

func (d *Disk) usage() (int, error) {
	if d.used != nil {
		return d.used(d.Mount)
	}
	return modules.DiskUsedPercent(d.Mount)
}

// Service raises an event when a service stops running, and again when it
// comes back.
type Service struct {
	Service string
	Every   time.Duration

	running func(name string) (bool, error) // swapped in tests
	state   state
	errored bool
}

func (s *Service) Name() string            { return "service" }
func (s *Service) Interval() time.Duration { return s.Every }

func (s *Service) Check() []Emission {
	up, err := s.isRunning()
	if err != nil {
		// A failed check is its own condition: it must not flip the
		// up/down edge, or a transient error reads as a stop followed by
		// a phantom recovery.
		if !s.errored {
			s.errored = true
			return []Emission{{Data: map[string]any{
				"service": s.Service, "error": err.Error(),
			}}}
		}
		return nil
	}
	s.errored = false
	if !s.state.transition(!up) {
		return nil
	}
	return []Emission{{Data: map[string]any{
		"service": s.Service,
		"running": up,
	}}}
}

func (s *Service) isRunning() (bool, error) {
	if s.running != nil {
		return s.running(s.Service)
	}
	return modules.ServiceRunning(s.Service)
}

// File raises an event when a watched file appears, changes, or is
// removed. Content is compared by digest, so a rewrite with identical
// bytes is correctly not a change.
type File struct {
	Path  string
	Every time.Duration

	started bool
	digest  string // empty means absent
	errored bool
}

func (f *File) Name() string            { return "file" }
func (f *File) Interval() time.Duration { return f.Every }

func (f *File) Check() []Emission {
	digest, err := fileDigest(f.Path)
	if err != nil && !os.IsNotExist(err) {
		// A file that cannot be read is not a file that was removed: hold
		// the baseline instead of reporting a phantom removed/created
		// pair, and say so once.
		if !f.errored {
			f.errored = true
			return []Emission{{Data: map[string]any{
				"path": f.Path, "error": err.Error(),
			}}}
		}
		return nil
	}
	f.errored = false
	if err != nil {
		digest = "" // absent
	}

	// The first check establishes the baseline without reporting: the file
	// existing at startup is not a change.
	if !f.started {
		f.started = true
		f.digest = digest
		return nil
	}
	if digest == f.digest {
		return nil
	}

	change := "changed"
	switch {
	case f.digest == "":
		change = "created"
	case digest == "":
		change = "removed"
	}
	f.digest = digest

	data := map[string]any{"path": f.Path, "change": change}
	if digest != "" {
		data["sha256"] = digest
	}
	return []Emission{{Data: data}}
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// build turns one parsed config entry into a beacon.
func build(kind string, cfg config) (Beacon, error) {
	interval, err := parseInterval(cfg.str("interval"))
	if err != nil {
		return nil, err
	}
	switch kind {
	case "disk":
		mount := cfg.str("mount")
		if mount == "" {
			mount = "/"
		}
		threshold, err := cfg.intOr("threshold", 90)
		if err != nil {
			return nil, err
		}
		if threshold < 1 || threshold > 100 {
			return nil, fmt.Errorf("threshold %d is not a percentage", threshold)
		}
		return &Disk{Mount: mount, Threshold: threshold, Every: interval}, nil
	case "service":
		name := cfg.str("name")
		if name == "" {
			return nil, fmt.Errorf("service beacon needs a name")
		}
		return &Service{Service: name, Every: interval}, nil
	case "file":
		path := cfg.str("path")
		if path == "" {
			return nil, fmt.Errorf("file beacon needs a path")
		}
		return &File{Path: path, Every: interval}, nil
	default:
		return nil, fmt.Errorf("unknown beacon %q (disk, service, file)", kind)
	}
}

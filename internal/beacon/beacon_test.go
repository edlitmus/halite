package beacon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestDiskBeaconIsEdgeTriggered(t *testing.T) {
	usage := 50
	d := &Disk{
		Mount:     "/var",
		Threshold: 90,
		used:      func(string) (int, error) { return usage, nil },
	}

	if got := d.Check(); len(got) != 0 {
		t.Fatalf("a healthy disk must be silent on the first check: %v", got)
	}
	if got := d.Check(); len(got) != 0 {
		t.Fatalf("still healthy, still silent: %v", got)
	}

	usage = 95
	fired := d.Check()
	if len(fired) != 1 {
		t.Fatalf("crossing the threshold must fire once: %v", fired)
	}
	if fired[0].Data["over"] != true || fired[0].Data["used"] != 95 {
		t.Errorf("data = %v", fired[0].Data)
	}

	// This is the whole point: a disk that stays full must not fire again.
	usage = 96
	if got := d.Check(); len(got) != 0 {
		t.Errorf("a disk that stays over threshold fired again: %v", got)
	}

	usage = 40
	cleared := d.Check()
	if len(cleared) != 1 || cleared[0].Data["over"] != false {
		t.Errorf("dropping back under must fire a clear: %v", cleared)
	}
}

func TestDiskBeaconReportsAnAlreadyFullDiskAtStartup(t *testing.T) {
	d := &Disk{
		Mount: "/", Threshold: 90,
		used: func(string) (int, error) { return 99, nil },
	}
	if fired := d.Check(); len(fired) != 1 {
		t.Errorf("a host that boots with a full disk must say so: %v", fired)
	}
}

func TestDiskBeaconReportsAnUnreadableMountOnce(t *testing.T) {
	d := &Disk{
		Mount: "/nope", Threshold: 90,
		used: func(string) (int, error) { return 0, fmt.Errorf("no such mount") },
	}
	fired := d.Check()
	if len(fired) != 1 || fired[0].Data["error"] == nil {
		t.Fatalf("an unreadable mount must fire once: %v", fired)
	}
	if got := d.Check(); len(got) != 0 {
		t.Errorf("and not once per check: %v", got)
	}
}

func TestServiceBeaconFiresOnStopAndRecovery(t *testing.T) {
	up := true
	s := &Service{
		Service: "nginx",
		running: func(string) (bool, error) { return up, nil },
	}

	if got := s.Check(); len(got) != 0 {
		t.Fatalf("a running service must be silent: %v", got)
	}

	up = false
	down := s.Check()
	if len(down) != 1 || down[0].Data["running"] != false {
		t.Fatalf("a stopped service must fire: %v", down)
	}
	if got := s.Check(); len(got) != 0 {
		t.Errorf("a service that stays down fired again: %v", got)
	}

	up = true
	back := s.Check()
	if len(back) != 1 || back[0].Data["running"] != true {
		t.Errorf("recovery must fire: %v", back)
	}
}

func TestFileBeaconTracksContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nginx.conf")
	if err := os.WriteFile(path, []byte("listen 80;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &File{Path: path}

	if got := f.Check(); len(got) != 0 {
		t.Fatalf("the first check is a baseline, not a change: %v", got)
	}

	if err := os.WriteFile(path, []byte("listen 8080;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := f.Check()
	if len(changed) != 1 || changed[0].Data["change"] != "changed" {
		t.Fatalf("an edit must fire: %v", changed)
	}
	if changed[0].Data["sha256"] == nil {
		t.Error("a change should carry the new digest")
	}

	if got := f.Check(); len(got) != 0 {
		t.Errorf("an unchanged file fired: %v", got)
	}

	// Rewriting identical bytes is not a change, however the mtime moves.
	if err := os.WriteFile(path, []byte("listen 8080;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.Check(); len(got) != 0 {
		t.Errorf("rewriting identical content fired: %v", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed := f.Check()
	if len(removed) != 1 || removed[0].Data["change"] != "removed" {
		t.Fatalf("removal must fire: %v", removed)
	}

	if err := os.WriteFile(path, []byte("back\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := f.Check()
	if len(created) != 1 || created[0].Data["change"] != "created" {
		t.Errorf("recreation must fire as created: %v", created)
	}
}

func TestFileBeaconOnAnAbsentFileIsSilentUntilItAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-yet")
	f := &File{Path: path}

	if got := f.Check(); len(got) != 0 {
		t.Fatalf("a file that has never existed is not news: %v", got)
	}
	if got := f.Check(); len(got) != 0 {
		t.Fatalf("still not news: %v", got)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.Check(); len(got) != 1 || got[0].Data["change"] != "created" {
		t.Errorf("appearing must fire: %v", got)
	}
}

func TestLoadParsesEveryKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacons.sls")
	content := `disk:
  - mount: /var
    threshold: "80"
    interval: 30s
  - mount: /
service:
  - name: nginx
    interval: 15s
file:
  - path: /usr/local/etc/nginx/nginx.conf
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	beacons, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(beacons) != 4 {
		t.Fatalf("got %d beacons, want 4", len(beacons))
	}

	first, ok := beacons[0].(*Disk)
	if !ok {
		t.Fatalf("first beacon is %T", beacons[0])
	}
	if first.Mount != "/var" || first.Threshold != 80 || first.Every != 30*time.Second {
		t.Errorf("first = %+v", first)
	}
	// The second disk entry takes the defaults.
	second := beacons[1].(*Disk)
	if second.Mount != "/" || second.Threshold != 90 || second.Every != DefaultInterval {
		t.Errorf("second = %+v", second)
	}
	if svc := beacons[2].(*Service); svc.Service != "nginx" || svc.Every != 15*time.Second {
		t.Errorf("service = %+v", svc)
	}
	if file := beacons[3].(*File); file.Path == "" {
		t.Errorf("file = %+v", file)
	}
}

func TestLoadAcceptsAMissingFile(t *testing.T) {
	beacons, err := Load(filepath.Join(t.TempDir(), "absent.sls"))
	if err != nil || beacons != nil {
		t.Errorf("a missing beacon file must be silent: %v, %v", beacons, err)
	}
	if beacons, err := Load(""); err != nil || beacons != nil {
		t.Errorf("an unset path must yield nothing: %v, %v", beacons, err)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]string{
		"unknown kind":      "cpu:\n  - threshold: \"90\"\n",
		"not a list":        "disk:\n  mount: /var\n",
		"empty list":        "disk: []\n",
		"service unnamed":   "service:\n  - interval: 30s\n",
		"file pathless":     "file:\n  - interval: 30s\n",
		"bad interval":      "disk:\n  - interval: soon\n",
		"interval too fast": "disk:\n  - interval: 100ms\n",
		"bad threshold":     "disk:\n  - threshold: lots\n",
		"threshold range":   "disk:\n  - threshold: \"140\"\n",
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "beacons.sls")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
}

// recording is a beacon that fires every check and counts them.
type recording struct {
	mu     sync.Mutex
	checks int
	every  time.Duration
}

func (r *recording) Name() string            { return "test" }
func (r *recording) Interval() time.Duration { return r.every }
func (r *recording) Check() []Emission {
	r.mu.Lock()
	r.checks++
	r.mu.Unlock()
	return []Emission{{Data: map[string]any{"n": 1}}}
}

func TestRunnerChecksImmediatelyThenOnInterval(t *testing.T) {
	b := &recording{every: 50 * time.Millisecond}
	var (
		mu    sync.Mutex
		fired int
	)
	emit := func(string, map[string]any) {
		mu.Lock()
		fired++
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go NewRunner([]Beacon{b}, emit, quietLogger()).Run(ctx)

	// The first check is immediate, so this should not need a full interval.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	immediate := fired
	mu.Unlock()
	if immediate < 1 {
		t.Error("the runner did not check on startup")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	mu.Lock()
	total := fired
	mu.Unlock()
	if total < 3 {
		t.Errorf("fired %d times in ~4 intervals, want at least 3", total)
	}
}

func TestRunnerStopsWithItsContext(t *testing.T) {
	b := &recording{every: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		NewRunner([]Beacon{b}, func(string, map[string]any) {}, quietLogger()).Run(ctx)
		close(stopped)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// exploding panics, standing in for a beacon with a bug in it.
type exploding struct{}

func (exploding) Name() string            { return "exploding" }
func (exploding) Interval() time.Duration { return time.Hour }
func (exploding) Check() []Emission       { panic("boom") }

func TestABrokenBeaconDoesNotTakeDownTheAgent(t *testing.T) {
	var logged strings.Builder
	runner := NewRunner([]Beacon{exploding{}}, func(string, map[string]any) {}, log.New(&logged, "", 0))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	runner.Run(ctx) // must return normally rather than propagate the panic

	if !strings.Contains(logged.String(), "panicked") {
		t.Errorf("the panic was not logged: %q", logged.String())
	}
}

func TestParseInterval(t *testing.T) {
	cases := map[string]struct {
		want    time.Duration
		wantErr bool
	}{
		"":      {DefaultInterval, false},
		"30s":   {30 * time.Second, false},
		"5m":    {5 * time.Minute, false},
		"1s":    {time.Second, false},
		"500ms": {0, true},
		"soon":  {0, true},
	}
	for raw, want := range cases {
		got, err := parseInterval(raw)
		if want.wantErr {
			if err == nil {
				t.Errorf("parseInterval(%q) succeeded, want an error", raw)
			}
			continue
		}
		if err != nil || got != want.want {
			t.Errorf("parseInterval(%q) = %v, %v; want %v", raw, got, err, want.want)
		}
	}
}

// A transient check error must not flip the up/down edge: recovery after an
// error, with the service never having stopped, is not a recovery.
func TestServiceBeaconErrorDoesNotFakeARecovery(t *testing.T) {
	up, fail := true, false
	s := &Service{
		Service: "nginx",
		running: func(string) (bool, error) {
			if fail {
				return false, fmt.Errorf("systemctl timed out")
			}
			return up, nil
		},
	}
	if got := s.Check(); len(got) != 0 {
		t.Fatalf("running service must be silent: %v", got)
	}
	fail = true
	if got := s.Check(); len(got) != 1 || got[0].Data["error"] == nil {
		t.Fatalf("first check error must fire once: %v", got)
	}
	fail = false
	if got := s.Check(); len(got) != 0 {
		t.Errorf("a service that never stopped must not report a recovery: %v", got)
	}
}

// A real threshold alert after a read error must still fire, with its data.
func TestDiskBeaconAlertsAfterAnError(t *testing.T) {
	fail := true
	d := &Disk{
		Mount: "/", Threshold: 90,
		used: func(string) (int, error) {
			if fail {
				return 0, fmt.Errorf("statfs failed")
			}
			return 95, nil
		},
	}
	if got := d.Check(); len(got) != 1 || got[0].Data["error"] == nil {
		t.Fatalf("read error must fire once: %v", got)
	}
	fail = false
	got := d.Check()
	if len(got) != 1 || got[0].Data["over"] != true {
		t.Fatalf("the threshold alert after an error must fire with data: %v", got)
	}
}

// An unreadable file is not a removed file.
func TestFileBeaconErrorIsNotARemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &File{Path: path}
	_ = f.Check() // baseline

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)
	got := f.Check()
	if len(got) == 1 && got[0].Data["change"] == "removed" {
		t.Fatalf("a read error was reported as a removal: %v", got)
	}
	if len(got) != 1 || got[0].Data["error"] == nil {
		t.Fatalf("a read error must fire once as an error: %v", got)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.Check(); len(got) != 0 {
		t.Errorf("unchanged content after a transient error fired: %v", got)
	}
}

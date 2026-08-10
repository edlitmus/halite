package mine

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mine.sls")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadParsesFunctionsAndIntervals(t *testing.T) {
	path := writeConfig(t, `network.interfaces:
  interval: 60s
disk.usage:
  interval: 5m
grains:
`)
	jobs, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}
	if jobs[0].Function != "network.interfaces" || jobs[0].Every != time.Minute {
		t.Errorf("first = %+v", jobs[0])
	}
	if jobs[1].Every != 5*time.Minute {
		t.Errorf("second = %+v", jobs[1])
	}
	// An entry with no body takes the default interval.
	if jobs[2].Function != FunctionGrains || jobs[2].Every != DefaultInterval {
		t.Errorf("third = %+v", jobs[2])
	}
}

func TestLoadRejectsFunctionsNothingCanPublish(t *testing.T) {
	// A typo here would otherwise be a function that silently never
	// publishes, which is worse than refusing to start.
	if _, err := Load(writeConfig(t, "netwrok.interfaces:\n")); err == nil {
		t.Error("an unknown function must be refused at load time")
	}
	// State functions are not publishable: they change things.
	if _, err := Load(writeConfig(t, "pkg.installed:\n")); err == nil {
		t.Error("a state function must not be publishable")
	}
}

func TestLoadRejectsBadIntervals(t *testing.T) {
	for name, content := range map[string]string{
		"unparseable": "grains:\n  interval: soon\n",
		"too short":   "grains:\n  interval: 10ms\n",
		"unknown key": "grains:\n  every: 30s\n",
		"not a map":   "grains: 30s\n",
	} {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
}

func TestLoadAcceptsAMissingFile(t *testing.T) {
	jobs, err := Load(filepath.Join(t.TempDir(), "absent.sls"))
	if err != nil || jobs != nil {
		t.Errorf("a missing mine config must be silent: %v, %v", jobs, err)
	}
	if jobs, err := Load(""); err != nil || jobs != nil {
		t.Errorf("an unset path must yield nothing: %v, %v", jobs, err)
	}
}

func TestRunnerPublishesGrainsImmediately(t *testing.T) {
	grains := map[string]any{"id": "web1", "os": "FreeBSD"}
	var (
		mu        sync.Mutex
		published []string
		last      map[string]any
	)
	publish := func(function string, data map[string]any) {
		mu.Lock()
		published = append(published, function)
		last = data
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go NewRunner([]Job{{Function: FunctionGrains, Every: time.Hour}}, grains, publish, quietLogger()).Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		count := len(published)
		mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("nothing was published on startup")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if published[0] != FunctionGrains {
		t.Errorf("published %q", published[0])
	}
	if last["os"] != "FreeBSD" {
		t.Errorf("data = %v", last)
	}
}

func TestRunnerRepublishesOnInterval(t *testing.T) {
	var (
		mu    sync.Mutex
		count int
	)
	publish := func(string, map[string]any) {
		mu.Lock()
		count++
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go NewRunner([]Job{{Function: FunctionGrains, Every: 50 * time.Millisecond}},
		map[string]any{}, publish, quietLogger()).Run(ctx)

	time.Sleep(250 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if count < 3 {
		t.Errorf("published %d times in ~5 intervals, want at least 3", count)
	}
}

func TestRunnerStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		NewRunner([]Job{{Function: FunctionGrains, Every: 20 * time.Millisecond}},
			map[string]any{}, func(string, map[string]any) {}, quietLogger()).Run(ctx)
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

func TestRunnerSurvivesAFunctionThatFails(t *testing.T) {
	// disk.usage on a host where it cannot work must not stop the loop or
	// publish nonsense.
	var published int
	publish := func(string, map[string]any) { published++ }
	runner := NewRunner(nil, map[string]any{}, publish, quietLogger())

	runner.publishOnce("nope.missing")
	if published != 0 {
		t.Error("a function that cannot be collected must publish nothing")
	}
}

func TestForTemplatesFlattensForTemplatePaths(t *testing.T) {
	raw := map[string]map[string]transport.MineEntry{
		"network.interfaces": {
			"web1": {Data: map[string]any{"lo0": "127.0.0.1"}, Updated: time.Now()},
			"web2": {Data: map[string]any{"lo0": "127.0.0.1"}, Updated: time.Now()},
		},
		"grains": {
			"web1": {Data: map[string]any{"os": "FreeBSD"}},
		},
	}
	out := ForTemplates(raw)

	// Dots become underscores so {{ .Mine.network_interfaces }} resolves.
	interfaces, ok := out["network_interfaces"].(map[string]any)
	if !ok {
		t.Fatalf("network_interfaces is %T", out["network_interfaces"])
	}
	if len(interfaces) != 2 {
		t.Errorf("got %d agents, want 2", len(interfaces))
	}
	web1, ok := interfaces["web1"].(map[string]any)
	if !ok || web1["lo0"] != "127.0.0.1" {
		t.Errorf("web1 = %v", interfaces["web1"])
	}
	if _, ok := out["grains"]; !ok {
		t.Errorf("a function without dots should keep its name: %v", out)
	}
}

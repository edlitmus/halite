package bridge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildEcho compiles the test extension once, and answers with its
// path.
//
// A real executable, run as a real child process. The property worth
// testing here is that two processes understand each other, and a mock
// of the protocol tests only that this package agrees with itself.
var (
	echoOnce sync.Once
	echoPath string
	echoErr  error
)

func echoExtension(t *testing.T) string {
	t.Helper()
	echoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "halite-bridge-*")
		if err != nil {
			echoErr = err
			return
		}
		echoPath = filepath.Join(dir, "echoext")
		build := exec.Command("go", "build", "-o", echoPath, "./testdata/echoext")
		build.Stderr = os.Stderr
		echoErr = build.Run()
	})
	if echoErr != nil {
		t.Fatalf("building the test extension: %v", echoErr)
	}
	return echoPath
}

func startEcho(t *testing.T, adjust func(*Options)) *Process {
	t.Helper()
	opts := Options{
		Path: echoExtension(t), Kind: "module",
		WorkDir: t.TempDir(), Timeout: 10 * time.Second,
		Stderr: func(line string) { t.Logf("extension stderr: %s", line) },
	}
	if adjust != nil {
		adjust(&opts)
	}
	proc, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proc.Close)
	return proc
}

func TestAnExtensionHandshakesAndAnswersACall(t *testing.T) {
	proc := startEcho(t, nil)

	info := proc.Info()
	if info.Name != "echo" || info.Version != "1.0.0" {
		t.Errorf("the extension identified as %s %s", info.Name, info.Version)
	}
	if len(info.Functions) != 1 {
		t.Errorf("it declared %d functions", len(info.Functions))
	}

	value, err := proc.Call(context.Background(), "say",
		nil, map[string]any{"message": "hello"},
		&CallContext{NodeID: "web1.example", Test: true})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	if got["said"] != "hello" {
		t.Errorf("it said %v", got["said"])
	}
	// The context reaches the extension, so an extension can honour
	// test mode the way a built-in module does.
	if got["node_id"] != "web1.example" || got["test"] != true {
		t.Errorf("the context did not arrive: %v", got)
	}
}

func TestStreamingFramesArriveBeforeTheResult(t *testing.T) {
	var mu sync.Mutex
	var logs, events []string
	var progress [][2]int

	proc := startEcho(t, func(o *Options) {
		o.OnLog = func(level, message string) {
			mu.Lock()
			logs = append(logs, level+":"+message)
			mu.Unlock()
		}
		o.OnProgress = func(done, total int, message string) {
			mu.Lock()
			progress = append(progress, [2]int{done, total})
			mu.Unlock()
		}
		o.OnEvent = func(tag string, data json.RawMessage) {
			mu.Lock()
			events = append(events, tag)
			mu.Unlock()
		}
	})

	value, err := proc.Call(context.Background(), "stream", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != `"streamed"` {
		t.Errorf("the result is %s", value)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 || logs[0] != "info:starting" {
		t.Errorf("the logs are %v", logs)
	}
	if len(progress) != 1 || progress[0] != [2]int{1, 2} {
		t.Errorf("the progress is %v", progress)
	}
	if len(events) != 1 || events[0] != "halite/ext/echo" {
		t.Errorf("the events are %v", events)
	}
}

// A function that fails is a failed call, not a dead process: the next
// call still works.
func TestAFailingFunctionDoesNotEndTheProcess(t *testing.T) {
	proc := startEcho(t, nil)

	if _, err := proc.Call(context.Background(), "fail", nil, nil, nil); err == nil {
		t.Fatal("a failing function reported success")
	}
	if dead, _ := proc.Dead(); dead {
		t.Fatal("a failing function killed the process")
	}
	if _, err := proc.Call(context.Background(), "say", nil,
		map[string]any{"message": "still here"}, nil); err != nil {
		t.Errorf("the process was unusable after a failure: %v", err)
	}
}

// A handler that panics produces a failed result rather than taking the
// extension down, so the host learns which function did it.
func TestAPanickingFunctionNamesItself(t *testing.T) {
	proc := startEcho(t, nil)

	_, err := proc.Call(context.Background(), "panic", nil, nil, nil)
	if err == nil {
		t.Fatal("a panicking function reported success")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("the failure is %v", err)
	}
	if dead, _ := proc.Dead(); dead {
		t.Error("a panicking function killed the process")
	}
}

// A hung extension cannot hang the agent. That is the whole reason
// SPEC 24.2 makes an extension a process.
func TestAHungExtensionIsKilled(t *testing.T) {
	proc := startEcho(t, func(o *Options) { o.Timeout = 500 * time.Millisecond })

	started := time.Now()
	_, err := proc.Call(context.Background(), "sleep", nil, map[string]any{"seconds": 30}, nil)
	if err == nil {
		t.Fatal("a hung call returned successfully")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the call took %s to give up", elapsed)
	}
	dead, reason := proc.Dead()
	if !dead {
		t.Error("the hung extension is still running")
	}
	if !strings.Contains(reason, "did not answer") {
		t.Errorf("it was killed for %q", reason)
	}
	// And it is not used again: the stream is where the extension left
	// it, and the next frame it sends answers a question nobody asked.
	if _, err := proc.Call(context.Background(), "say", nil, nil, nil); err == nil {
		t.Error("a killed process accepted another call")
	}
}

// An extension that sent something the host cannot read has lost track
// of the stream.
func TestAProtocolViolationKillsTheProcess(t *testing.T) {
	proc := startEcho(t, func(o *Options) { o.Timeout = 3 * time.Second })

	if _, err := proc.Call(context.Background(), "garbage", nil, nil, nil); err == nil {
		t.Fatal("a garbage frame was accepted")
	}
	if dead, _ := proc.Dead(); !dead {
		t.Error("the process survived a protocol violation")
	}
}

func TestAnExtensionThatExitsIsReportedAsSuch(t *testing.T) {
	proc := startEcho(t, nil)
	_, err := proc.Call(context.Background(), "exit", nil, nil, nil)
	if err == nil {
		t.Fatal("an extension that exited reported success")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("the failure is %v", err)
	}
}

// An extension inheriting the agent's environment inherits whatever
// credentials are in it.
func TestTheEnvironmentIsNotInherited(t *testing.T) {
	t.Setenv("HALITE_TEST_SECRET", "should-not-be-visible")
	proc := startEcho(t, func(o *Options) { o.Env = []string{"PATH=/usr/bin"} })

	value, err := proc.Call(context.Background(), "environment", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Vars []string `json:"vars"`
	}
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	for _, v := range got.Vars {
		if strings.Contains(v, "should-not-be-visible") {
			t.Errorf("the host's environment reached the extension: %s", v)
		}
	}
}

// The network is denied unless the extension declared it.
func TestTheNetworkIsNotGrantedByDefault(t *testing.T) {
	proc := startEcho(t, func(o *Options) {
		o.Sandbox = DefaultSandbox()
		o.Env = []string{"PATH=/usr/bin"}
	})
	value, err := proc.Call(context.Background(), "environment", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Denied bool `json:"network_denied"`
	}
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Denied {
		t.Error("the extension was not told the network is denied")
	}

	// And an extension that declared it is told otherwise.
	sandbox, err := From([]string{"network"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	granted := startEcho(t, func(o *Options) {
		o.Sandbox = sandbox
		o.Env = []string{"PATH=/usr/bin"}
	})
	value, err = granted.Call(context.Background(), "environment", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(value, &got)
	if got.Denied {
		t.Error("an extension that declared the network was told it is denied")
	}
}

// An extension asking for something the host cannot enforce must not
// run as though it had been granted.
func TestAnUnknownDeclarationIsRefused(t *testing.T) {
	if _, err := From([]string{"raw_sockets"}, "", ""); err == nil {
		t.Fatal("an unknown declaration was accepted")
	}
}

// The limits reach the extension and it applies them to itself.
func TestResourceLimitsReachTheExtension(t *testing.T) {
	sandbox := DefaultSandbox()
	sandbox.OpenFiles = 64
	proc := startEcho(t, func(o *Options) {
		o.Sandbox = sandbox
		o.Env = []string{"PATH=/usr/bin"}
	})

	value, err := proc.Call(context.Background(), "limits", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		NoFile string `json:"nofile"`
	}
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	if !limitsSupported() {
		t.Skip("this platform has no setrlimit")
	}
	if got.NoFile != "64" {
		t.Errorf("the extension is running with an open-file limit of %s, want 64", got.NoFile)
	}
}

// `Describe` is what an operator reads. It must say what is actually
// enforced here, not what SPEC 24.3's table hopes for across five
// operating systems.
func TestTheSandboxDescribesWhatItReallyDoes(t *testing.T) {
	described := strings.Join(DefaultSandbox().Describe(), "; ")
	for _, want := range []string{"process boundary", "network", "not built"} {
		if !strings.Contains(described, want) {
			t.Errorf("the description is missing %q: %s", want, described)
		}
	}
	// A nil sandbox is the development case and says so plainly.
	var none *Sandbox
	if !strings.Contains(strings.Join(none.Describe(), " "), "host's own identity") {
		t.Errorf("an unsandboxed extension is described as %v", none.Describe())
	}
}

func TestAPoolServesConcurrentCalls(t *testing.T) {
	pool := NewPool(Options{
		Path: echoExtension(t), Kind: "module",
		WorkDir: t.TempDir(), Timeout: 10 * time.Second,
	}, 3)
	defer pool.Close()

	const callers = 6
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := pool.Call(context.Background(), "sleep", nil,
				map[string]any{"seconds": 0.2}, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a pooled call failed: %v", err)
		}
	}

	info, known := pool.Info()
	if !known || info.Name != "echo" {
		t.Errorf("the pool did not learn what the extension is: %+v", info)
	}
}

// A process killed for a timeout is not reused. Recovery is a fork.
func TestAPoolReplacesAProcessThatDied(t *testing.T) {
	pool := NewPool(Options{
		Path: echoExtension(t), Kind: "module",
		WorkDir: t.TempDir(), Timeout: 500 * time.Millisecond,
	}, 1)
	defer pool.Close()

	if _, err := pool.Call(context.Background(), "sleep", nil,
		map[string]any{"seconds": 30}, nil); err == nil {
		t.Fatal("a hung call succeeded")
	}
	// The pool is back to full strength: a fresh process answers.
	if _, err := pool.Call(context.Background(), "say", nil,
		map[string]any{"message": "recovered"}, nil); err != nil {
		t.Errorf("the pool did not replace the killed process: %v", err)
	}
}

// A frame that claims a gigabyte is refused before anything is
// allocated for it.
func TestAnEnormousFrameIsRefusedBeforeAllocation(t *testing.T) {
	header := []byte{0x7f, 0xff, 0xff, 0xff}
	_, err := ReadFrame(strings.NewReader(string(header)))
	if err == nil {
		t.Fatal("an enormous frame was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the refusal is %v", err)
	}
}

// A frame boundary must not depend on an extension never emitting a
// newline inside a string.
func TestAFrameSurvivesAwkwardContent(t *testing.T) {
	var b strings.Builder
	awkward := Frame{Kind: KindLog, Message: "line one\nline two\r\n{\"kind\":\"result\"}"}
	if err := WriteFrame(&b, awkward); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != awkward.Message {
		t.Errorf("the message came back as %q", got.Message)
	}
}

// RLIMIT_AS bounds virtual address space, and a garbage-collected
// runtime reserves far more of that than it commits. A Go extension
// under a 512 MiB RLIMIT_AS — which reads as generous — dies with "out
// of memory" after allocating about 160 MiB.
//
// It is available, because SPEC 24.3 names it, and it is off by
// default, because a default that kills extensions intermittently is
// worse than no default.
func TestTheDefaultSandboxDoesNotSetAnAddressSpaceLimit(t *testing.T) {
	if got := DefaultSandbox().MemoryBytes; got != 0 {
		t.Errorf("the default sets RLIMIT_AS to %d, which kills a Go extension", got)
	}
	described := strings.Join(DefaultSandbox().Describe(), "; ")
	if !strings.Contains(described, "unbounded") {
		t.Errorf("the description does not say the address space is unbounded: %s", described)
	}

	// Set deliberately, it reaches the extension.
	sandbox := DefaultSandbox()
	sandbox.MemoryBytes = 2 << 30
	if !strings.Contains(strings.Join(sandbox.Describe(), "; "), "2048 MiB") {
		t.Errorf("a set limit is not described: %v", sandbox.Describe())
	}
}

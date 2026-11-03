package fnmatch

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"
)

// A glob that reaches this package from a request -- a target, a `G@`
// pattern -- was compiled to a regexp with no bound on its length and kept
// in a map that never forgot it. Measured on main de26c91b: one 1 MiB
// target allocated some 550 MB while compiling, and three distinct 1 MiB
// patterns left 158 MB held for the life of the hub. A pattern RE2 refuses
// (`?` four million times, measured) was a panic, because the compile was
// MustCompile. DIVERGENCE 5.558.

func TestCompileRefusesAnOverLongPattern(t *testing.T) {
	// `*a` is the dearest shape measured per character.
	at := strings.Repeat("*a", MaxPatternLen/2)
	p, err := Compile(at)
	if err != nil {
		t.Fatalf("a pattern of exactly %d bytes: %v", MaxPatternLen, err)
	}
	if !p.Match(strings.Repeat("xa", MaxPatternLen/2)) || p.Match("b") {
		t.Fatal("the pattern at the limit does not match as fnmatch would")
	}

	over := at + "*"
	_, err = Compile(over)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("a pattern of %d bytes: want ErrTooLong, got %v", len(over), err)
	}
	// The error is read by a person and goes into a log line and an
	// API response: it says how long the pattern was, and does not
	// repeat it. RE2's own "expression too large" error quotes the
	// whole translated expression, which for the pattern that reaches
	// it is megabytes.
	if len(err.Error()) > 200 {
		t.Fatalf("the error is %d bytes; it should not quote the pattern", len(err.Error()))
	}
}

// A pattern with no `*?[` is the string itself, so it needs no regexp and
// no limit: a long literal costs what the string costs. This is what keeps
// a long list of node names written as a compound (DIVERGENCE 5.291) at
// the price it had before globs were compiled.
func TestALiteralPatternIsNotCompiled(t *testing.T) {
	long := strings.Repeat("host-", 10*MaxPatternLen)
	p, err := Compile(long)
	if err != nil {
		t.Fatalf("a literal of %d bytes: %v", len(long), err)
	}
	if p.re != nil {
		t.Fatal("a literal was compiled to a regexp")
	}
	if !p.Match(long) || p.Match(long+"x") || p.Match(long[1:]) {
		t.Fatal("a literal pattern does not match only itself")
	}
	// Match, the configuration path, agrees and caches nothing for it.
	resetCache()
	if !Match(long, long) || Match(long, "host") {
		t.Fatal("Match on a literal does not match only itself")
	}
	if n, _ := cacheStats(); n != 0 {
		t.Fatalf("a literal took %d cache entries", n)
	}
}

// Invalid UTF-8 is where "a literal is itself" and the regexp could
// differ: []rune turns a bad byte into U+FFFD, and the regexp reads one in
// the input the same way. The fast path is taken only for valid UTF-8, so
// the two answers stay the regexp's.
func TestALiteralWithInvalidUTF8AgreesWithTheRegexp(t *testing.T) {
	for _, c := range []struct{ pattern, name string }{
		{"\xff", "\xff"},
		{"\xff", "�"},
		{"a\xfe", "a\xfe"},
		{"a\xfe", "a\xff"},
	} {
		want := regexp.MustCompile(fnmatchTranslate(c.pattern)).MatchString(c.name)
		p, err := Compile(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Match(c.name); got != want {
			t.Errorf("Compile(%q).Match(%q) = %v; the regexp says %v", c.pattern, c.name, got, want)
		}
		if got := Match(c.pattern, c.name); got != want {
			t.Errorf("Match(%q, %q) = %v; the regexp says %v", c.pattern, c.name, got, want)
		}
	}
}

// RE2 refusing a translation is an error from Compile and "no match" from
// Match, never a panic. The only translation RE2 has been seen to refuse
// is millions of characters long (`?` x 4,000,000: "expression too large",
// after 1.3 GB allocated), far past MaxPatternLen and too dear to run
// here, so the refusal is injected.
func TestAPatternRE2RefusesIsAnErrorNotAPanic(t *testing.T) {
	saved := compileRegexp
	t.Cleanup(func() { compileRegexp = saved; resetCache() })
	compileRegexp = func(string) (*regexp.Regexp, error) {
		return nil, errors.New("error parsing regexp: expression too large: `" + strings.Repeat(".", 10_000) + "`")
	}
	resetCache()

	_, err := Compile("web*")
	if err == nil {
		t.Fatal("Compile accepted a pattern the regexp compiler refused")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("the error is %d bytes; it should not carry RE2's quotation of the expression", len(err.Error()))
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Match panicked: %v", r)
		}
	}()
	if Match("web*", "web1") {
		t.Fatal("a pattern that does not compile matched")
	}
	if n, _ := cacheStats(); n != 0 {
		t.Fatalf("a pattern that did not compile took %d cache entries", n)
	}
}

// The cache Match keeps for configuration patterns holds at most
// cacheBytes of pattern text, however many distinct patterns pass
// through it, and keeps the one in use.
func TestTheCacheIsBounded(t *testing.T) {
	resetCache()
	t.Cleanup(resetCache)
	// Some 150 KB of distinct patterns, more than twice the bound.
	pad := strings.Repeat("x", 20)
	for i := range 5000 {
		Match(fmt.Sprintf("web%05d-%s*", i, pad), "web1")
		if i%7 == 0 {
			// Looked at before it is used again: a cache that does
			// not move a hit to the front evicts it in time, used or
			// not.
			if i > 0 && !cached("db*") {
				t.Fatalf("after %d patterns, the pattern used every seventh was evicted: the cache is not least-recently-used", i)
			}
			Match("db*", "db1")
		}
	}
	n, bytes := cacheStats()
	if bytes > cacheBytes {
		t.Fatalf("the cache holds %d bytes of patterns; the bound is %d", bytes, cacheBytes)
	}
	if n == 0 || n > cacheBytes {
		t.Fatalf("the cache holds %d entries", n)
	}
	// A pattern bigger than the whole cache is compiled and matched, and
	// not kept.
	big := strings.Repeat("*a", MaxPatternLen/2)
	if !Match(big, strings.Repeat("a", MaxPatternLen)) {
		t.Fatal("a pattern at the limit did not match")
	}
}

// The cache is shared by every goroutine that matches, and evicts while
// they do: `make race` is what holds this one.
func TestTheCacheIsSafeForConcurrentUse(t *testing.T) {
	resetCache()
	t.Cleanup(resetCache)
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 3000 {
				p := fmt.Sprintf("web%05d-xxxxxxxxxxxxxxxxxxxx*", (i*7+g)%4000)
				if !Match(p, p[:len(p)-1]+"z") {
					t.Errorf("%q did not match", p)
					return
				}
			}
		}()
	}
	wg.Wait()
	if _, bytes := cacheStats(); bytes > cacheBytes {
		t.Fatalf("the cache holds %d bytes; the bound is %d", bytes, cacheBytes)
	}
}

const cacheHelperEnv = "HALITE_FNMATCH_CACHE_HELPER"

// TestCacheMemoryHelper is the child for TestDistinctPatternsDoNotHoldMemory.
func TestCacheMemoryHelper(t *testing.T) {
	if os.Getenv(cacheHelperEnv) == "" {
		t.Skip("child process for TestDistinctPatternsDoNotHoldMemory; runs only when that test starts it")
	}
	// A soft limit, so that the old code's growth is paid for in GC
	// work inside this child, not in the machine's memory.
	debug.SetMemoryLimit(512 << 20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	// 300 distinct patterns of the dearest shape at the length limit.
	// Measured on darwin/arm64, go1.27.3, one of these holds some
	// 120 KB compiled, so a cache that keeps them all holds ~37 MB.
	for i := range 300 {
		p := fmt.Sprintf("%04d", i) + strings.Repeat("*a", MaxPatternLen/2-2)
		Match(p, "x")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	fmt.Printf("HELD: %d\n", held)
}

// TestDistinctPatternsDoNotHoldMemory is the heap measured rather than
// the cache's own account of itself, in a child process so that the old
// code's growth is not this process's.
func TestDistinctPatternsDoNotHoldMemory(t *testing.T) {
	if os.Getenv(cacheHelperEnv) != "" {
		t.Skip("in the child")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCacheMemoryHelper$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), cacheHelperEnv+"=1", "GOMAXPROCS=2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var held int64
	i := strings.Index(string(out), "HELD: ")
	if i < 0 {
		t.Fatalf("the child reported nothing:\n%s", out)
	}
	fmt.Sscanf(string(out[i:]), "HELD: %d", &held)
	// cacheBytes of pattern text at the dearest measured ratio, with
	// room: the bound is what is being held, not the exact figure.
	if limit := int64(16 << 20); held > limit {
		t.Fatalf("300 distinct patterns left %d MB held; the bound is %d MB", held>>20, limit>>20)
	}
	t.Logf("held after 300 distinct patterns: %d KB", held>>10)
}

func resetCache() {
	cache.Lock()
	defer cache.Unlock()
	cache.order.Init()
	cache.byPat = map[string]*list.Element{}
	cache.bytes = 0
}

func cacheStats() (entries, bytes int) {
	cache.Lock()
	defer cache.Unlock()
	return cache.order.Len(), cache.bytes
}

func cached(pattern string) bool {
	cache.Lock()
	defer cache.Unlock()
	_, ok := cache.byPat[pattern]
	return ok
}

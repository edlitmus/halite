package builtin

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The five password-ageing arguments, which this build did not have.
//
// An estate's real tree sets them on three accounts, and without them
// those declarations did not compile: eleven of the twenty-nine errors
// its highstate produced were these five names. They are shadow(5)
// columns and chage(1) options, and the mapping between the two is what
// these tests pin.

func i64(n int64) *int64 { return &n }

func TestAgingArgvSetsOnlyWhatDiffers(t *testing.T) {
	have := shadowAging{Min: i64(0), Max: i64(99999), Warn: i64(7)}

	// Nothing asked for is nothing to do.
	if got := agingArgv("bob", shadowAging{}, have); got != nil {
		t.Errorf("an empty request produced %v, want no command", got)
	}
	// A value that already matches is not re-set, which is what keeps a
	// converged run from reporting a change every time.
	if got := agingArgv("bob", shadowAging{Warn: i64(7)}, have); got != nil {
		t.Errorf("a matching value produced %v, want no command", got)
	}
	// Only the differing columns reach chage, each with its own flag.
	got := agingArgv("bob", shadowAging{Min: i64(1), Max: i64(90), Warn: i64(7)}, have)
	want := []string{"chage", "-m", "1", "-M", "90", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agingArgv = %v, want %v", got, want)
	}
}

// Each argument has to reach its own chage option. Swapping two of them
// silently applies the wrong policy, which is the failure worth pinning.
func TestEachAgingArgumentMapsToItsOwnFlag(t *testing.T) {
	for _, c := range []struct {
		arg, flag string
		want      shadowAging
	}{
		{"mindays", "-m", shadowAging{Min: i64(3)}},
		{"maxdays", "-M", shadowAging{Max: i64(3)}},
		{"warndays", "-W", shadowAging{Warn: i64(3)}},
		{"inactdays", "-I", shadowAging{Inact: i64(3)}},
		{"expire", "-E", shadowAging{Expire: i64(3)}},
	} {
		got := agingFrom(value.MapOf(c.arg, int64(3)))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s parsed to %+v, want %+v", c.arg, got, c.want)
		}
		argv := agingArgv("bob", got, shadowAging{})
		if len(argv) != 4 || argv[1] != c.flag || argv[2] != "3" {
			t.Errorf("%s produced %v, want the %s flag", c.arg, argv, c.flag)
		}
	}
}

// Zero and -1 are both meaningful to chage -- "immediately" and "never"
// -- so an unmentioned argument has to be distinguishable from a zero
// one. That is why the fields are pointers.
func TestAnUnsetAgeingArgumentIsNotZero(t *testing.T) {
	if agingRequested(value.MapOf("name", "bob")) {
		t.Error("an account with no ageing arguments was treated as requesting some")
	}
	zero := agingFrom(value.MapOf("mindays", int64(0)))
	if zero.Min == nil || *zero.Min != 0 {
		t.Fatalf("mindays: 0 parsed to %+v, want a pointer to zero", zero.Min)
	}
	// Against a shadow entry whose column is unset, zero is a change.
	if got := agingArgv("bob", zero, shadowAging{}); len(got) == 0 {
		t.Error("setting mindays to 0 on an unset column was treated as no change")
	}
	// And -1, which is how chage spells "never".
	never := agingFrom(value.MapOf("maxdays", int64(-1)))
	if never.Max == nil || *never.Max != -1 {
		t.Errorf("maxdays: -1 parsed to %+v", never.Max)
	}
}

// A tree that derives a value from pillar hands over a string.
func TestAgeingAcceptsANumberWrittenAsAString(t *testing.T) {
	got := agingFrom(value.MapOf("maxdays", "90"))
	if got.Max == nil || *got.Max != 90 {
		t.Errorf(`maxdays: "90" parsed to %+v, want 90`, got.Max)
	}
}

// The four columns FreeBSD has no equivalent for are refused by name
// rather than accepted and dropped. Accepting an argument and applying
// nothing is the defect shape this project keeps finding.
func TestAgeingNamesThePlatformThatCannotDoIt(t *testing.T) {
	why := agingUnsupported(value.MapOf("maxdays", int64(90), "mindays", int64(1)))
	if runtimeIsLinux() {
		if why != "" {
			t.Errorf("Linux refused password ageing: %s", why)
		}
		return
	}
	if why == "" {
		t.Fatal("a platform without chage accepted password ageing")
	}
	for _, want := range []string{"maxdays", "mindays", "login.conf"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not mention %q: %s", want, why)
		}
	}
}

func runtimeIsLinux() bool { return runtime.GOOS == "linux" }

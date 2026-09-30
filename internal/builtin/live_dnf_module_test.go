package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The dnf_module live tests. Run on 2026-09-30 as root on the lab's
// rocky9 (Rocky Linux 9.8, dnf 4.14.0) and alma8 (AlmaLinux 8.10, dnf
// 4.7.0); see evidence.go's note for what that did and did not cover.

func requireDnf(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("dnf module streams are EL8+; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("dnf") == "" {
		t.Skip("this host has no dnf")
	}
	return c
}

func requireDnfRoot(t *testing.T) *exec.Context {
	t.Helper()
	c := requireDnf(t)
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this enable, disable and reset real module streams")
	}
	if os.Geteuid() != 0 {
		t.Skip("dnf module enable/disable/reset needs root")
	}
	return c
}

func dnfLiveList(t *testing.T, r *Registries, c *exec.Context, args *value.Map) []*value.Map {
	t.Helper()
	raw, err := r.Exec.Call(c, "dnf_module.list", args)
	if err != nil {
		t.Fatalf("dnf_module.list: %v", err)
	}
	var out []*value.Map
	for _, row := range raw.([]any) {
		out = append(out, row.(*value.Map))
	}
	return out
}

func dnfLiveStatus(t *testing.T, r *Registries, c *exec.Context) *value.Map {
	t.Helper()
	raw, err := r.Exec.Call(c, "dnf_module.status", value.NewMap(0))
	if err != nil {
		t.Fatalf("dnf_module.status: %v", err)
	}
	return raw.(*value.Map)
}

func dnfStr(m *value.Map, key string) string {
	v, _ := m.Get(key)
	s, _ := v.(string)
	return s
}

func dnfBool(m *value.Map, key string) bool {
	v, _ := m.Get(key)
	b, _ := v.(bool)
	return b
}

func dnfStrs(m *value.Map, key string) []string {
	v, _ := m.Get(key)
	var out []string
	for _, item := range v.([]any) {
		out = append(out, item.(string))
	}
	return out
}

// dnfLiveModuleState reads one module's persisted state, or "" when
// status does not list it.
func dnfLiveModuleState(t *testing.T, r *Registries, c *exec.Context, name string) (state, stream string, profiles []string) {
	t.Helper()
	entry, ok := dnfLiveStatus(t, r, c).Get(name)
	if !ok {
		return "", "", nil
	}
	m := entry.(*value.Map)
	return dnfStr(m, "state"), dnfStr(m, "stream"), dnfStrs(m, "profiles")
}

// TestLiveDnfModuleReadersAgree holds `list`, which asks dnf, to
// `status`, which reads modules.d without it -- two answers to "what has
// this host chosen" that must say the same thing. Read-only, so it needs
// neither root nor the gate, the way `pro`'s reading test does not.
func TestLiveDnfModuleReadersAgree(t *testing.T) {
	c := requireDnf(t)
	r := New()
	rows := dnfLiveList(t, r, c, value.NewMap(0))
	if len(rows) == 0 {
		t.Fatal("dnf_module.list returned no streams at all; every EL8/EL9 AppStream has some")
	}
	status := dnfLiveStatus(t, r, c)
	checkAgreement(t, rows, status, "")
}

// checkAgreement compares the two readers. `only`, when set, is the one
// module `rows` was listed for, so the rest of modules.d is not held to a
// listing that never asked about it.
func checkAgreement(t *testing.T, rows []*value.Map, status *value.Map, only string) {
	t.Helper()
	enabledInList := map[string]*value.Map{}
	disabledRows, rowsOf := map[string]int{}, map[string]int{}
	for _, row := range rows {
		name := dnfStr(row, "name")
		rowsOf[name]++
		if dnfBool(row, "enabled") {
			enabledInList[name] = row
		}
		if dnfBool(row, "disabled") {
			disabledRows[name]++
		}
	}
	for _, key := range status.Keys() {
		name := key.(string)
		if only != "" && name != only {
			continue
		}
		m, _ := status.Get(name)
		st := m.(*value.Map)
		switch dnfStr(st, "state") {
		case "enabled":
			row, ok := enabledInList[name]
			if !ok {
				t.Errorf("modules.d says %s is enabled; dnf module list marks no stream of it [e]", name)
				continue
			}
			if dnfStr(row, "stream") != dnfStr(st, "stream") {
				t.Errorf("%s: modules.d says stream %q, dnf module list marks %q [e]",
					name, dnfStr(st, "stream"), dnfStr(row, "stream"))
			}
			if got, want := strings.Join(dnfStrs(row, "installed_profiles"), ","),
				strings.Join(dnfStrs(st, "profiles"), ","); got != want {
				t.Errorf("%s: dnf module list marks %q installed, modules.d says %q", name, got, want)
			}
		case "disabled":
			if rowsOf[name] == 0 || disabledRows[name] != rowsOf[name] {
				t.Errorf("modules.d says %s is disabled; dnf module list marks %d of its %d streams [x]",
					name, disabledRows[name], rowsOf[name])
			}
		}
	}
	for name := range enabledInList {
		if _, ok := status.Get(name); !ok {
			t.Errorf("dnf module list marks %s [e]; modules.d has no enabled entry for it", name)
		}
	}
}

// dnfLiveRestore puts a module back the way it was found: reset through
// dnf itself, then remove the file reset leaves behind if there was none
// before -- `state=` and no file mean the same to dnf, but "restored" is
// meant literally here.
func dnfLiveRestore(t *testing.T, c *exec.Context, name string, hadFile bool) {
	t.Helper()
	if _, err := c.Run(exec.Command{Argv: []string{"dnf", "-y", "-q", "module", "reset", name}}); err != nil {
		t.Errorf("restoring %s: %v", name, err)
	}
	if !hadFile {
		path := filepath.Join(DnfModulesDir, name+".module")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restoring %s: %v", name, err)
		}
	}
}

// dnfLiveUntouched skips unless the host has made no choice about the
// module and has none of it installed: this test must never overwrite
// a stream somebody chose.
func dnfLiveUntouched(t *testing.T, r *Registries, c *exec.Context, name, pkg string) bool {
	t.Helper()
	if state, stream, _ := dnfLiveModuleState(t, r, c, name); state != "" {
		t.Skipf("this host has %s %s (stream %q); not touching a real choice", name, state, stream)
	}
	if res, err := c.Run(exec.Command{Argv: []string{"rpm", "-q", pkg}, IgnoreExitCode: true}); err != nil || res.Code == 0 {
		t.Skipf("%s is installed here (err=%v); not touching it", pkg, err)
	}
	_, err := os.Stat(filepath.Join(DnfModulesDir, name+".module"))
	return err == nil
}

// TestLiveDnfModuleEnableSwitchDisableReset drives nginx -- a module on
// both EL8 and EL9 with several streams and nothing installed -- through
// every stream-state verb, checking each step through both readers.
// Nothing is installed: enable, switch-to, disable and reset change only
// what modules.d says.
func TestLiveDnfModuleEnableSwitchDisableReset(t *testing.T) {
	c := requireDnfRoot(t)
	r := New()
	const name = "nginx"
	hadFile := dnfLiveUntouched(t, r, c, name, name)

	var streams []string
	for _, row := range dnfLiveList(t, r, c, value.MapOf("name", name)) {
		if !dnfBool(row, "default") {
			streams = append(streams, dnfStr(row, "stream"))
		}
	}
	if len(streams) < 2 {
		t.Skipf("nginx offers %d non-default streams here; this test needs two", len(streams))
	}
	first, second := streams[0], streams[len(streams)-1]
	defer dnfLiveRestore(t, c, name, hadFile)

	call := func(fn, spec string) *value.Map {
		t.Helper()
		out, err := r.Exec.Call(c, "dnf_module."+fn, value.MapOf("modules", []any{spec}))
		if err != nil {
			t.Fatalf("dnf_module.%s %s: %v", fn, spec, err)
		}
		return out.(*value.Map)
	}
	expect := func(step, wantState, wantStream string) {
		t.Helper()
		state, stream, _ := dnfLiveModuleState(t, r, c, name)
		if state != wantState || stream != wantStream {
			t.Fatalf("after %s, status says %s is %q stream %q; want %q stream %q",
				step, name, state, stream, wantState, wantStream)
		}
		checkAgreement(t, dnfLiveList(t, r, c, value.MapOf("name", name)), dnfLiveStatus(t, r, c), name)
	}

	out := call("enable", name+":"+first)
	changes, _ := out.Get("changes")
	if _, ok := changes.(*value.Map).Get(name); !ok {
		t.Errorf("enable reported changes %v, want an entry for %s", changes, name)
	}
	expect("enable", "enabled", first)

	// dnf refuses to change an enabled stream by `enable`; the refusal
	// must reach the caller with dnf's own reason, and change nothing.
	_, err := r.Exec.Call(c, "dnf_module.enable", value.MapOf("modules", []any{name + ":" + second}))
	if err == nil || !strings.Contains(err.Error(), "switch") {
		t.Errorf("enabling a second stream: err = %v, want dnf's refusal to switch", err)
	}
	expect("the refused enable", "enabled", first)

	call("switch_to", name+":"+second)
	expect("switch_to", "enabled", second)

	call("disable", name)
	expect("disable", "disabled", "")

	out = call("reset", name)
	changes, _ = out.Get("changes")
	entry, _ := changes.(*value.Map).Get(name)
	if entry == nil {
		t.Fatalf("reset reported no change for %s: %v", name, changes)
	}
	if neu, _ := entry.(*value.Map).Get("new"); neu != nil {
		t.Errorf("reset's new state = %v, want nil: a reset module is no choice at all", neu)
	}
	expect("reset", "", "")
}

// TestLiveDnfModuleInstallAndRemove installs one module profile and
// removes it again. redis's `common` profile is one package with no
// dependencies on both EL8 and EL9 (measured with --assumeno first:
// 1.6 MB on rocky9, 1.2 MB on alma8).
func TestLiveDnfModuleInstallAndRemove(t *testing.T) {
	c := requireDnfRoot(t)
	r := New()
	const name = "redis"
	hadFile := dnfLiveUntouched(t, r, c, name, name)

	rows := dnfLiveList(t, r, c, value.MapOf("name", name))
	if len(rows) == 0 {
		t.Skip("this host's repositories offer no redis module")
	}
	stream := dnfStr(rows[len(rows)-1], "stream")
	spec := name + ":" + stream + "/common"
	defer func() {
		if res, _ := c.Run(exec.Command{Argv: []string{"rpm", "-q", name}, IgnoreExitCode: true}); res.Code == 0 {
			if _, err := c.Run(exec.Command{Argv: []string{"dnf", "-y", "-q", "remove", name}}); err != nil {
				t.Errorf("restoring: removing %s: %v", name, err)
			}
		}
		dnfLiveRestore(t, c, name, hadFile)
	}()

	rpmInstalled := func() bool {
		res, err := c.Run(exec.Command{Argv: []string{"rpm", "-q", name}, IgnoreExitCode: true})
		return err == nil && res.Code == 0
	}

	if _, err := r.Exec.Call(c, "dnf_module.install", value.MapOf("modules", []any{spec})); err != nil {
		t.Fatalf("dnf_module.install %s: %v", spec, err)
	}
	state, got, profiles := dnfLiveModuleState(t, r, c, name)
	if state != "enabled" || got != stream || strings.Join(profiles, ",") != "common" {
		t.Fatalf("after install, status = %q %q %v; want enabled %q [common]", state, got, profiles, stream)
	}
	if !rpmInstalled() {
		t.Fatalf("dnf_module.install succeeded and rpm -q %s says it is not installed", name)
	}
	installed := dnfLiveList(t, r, c, value.MapOf("filter", "installed"))
	found := false
	for _, row := range installed {
		if dnfStr(row, "name") == name && strings.Join(dnfStrs(row, "installed_profiles"), ",") == "common" {
			found = true
		}
	}
	if !found {
		t.Errorf("dnf_module.list filter=installed does not show %s with common [i]", spec)
	}
	checkAgreement(t, dnfLiveList(t, r, c, value.MapOf("name", name)), dnfLiveStatus(t, r, c), name)

	if _, err := r.Exec.Call(c, "dnf_module.remove", value.MapOf("modules", []any{spec})); err != nil {
		t.Fatalf("dnf_module.remove %s: %v", spec, err)
	}
	state, got, profiles = dnfLiveModuleState(t, r, c, name)
	if state != "enabled" || got != stream || len(profiles) != 0 {
		t.Errorf("after remove, status = %q %q %v; want still enabled %q with no profiles", state, got, profiles, stream)
	}
	if rpmInstalled() {
		t.Errorf("dnf_module.remove succeeded and %s is still installed", name)
	}
}

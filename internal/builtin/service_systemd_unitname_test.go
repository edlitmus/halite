package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// The unit-name canonicalisation, case by case. The dotted names are
// the defect: `php8.2-fpm` is the Debian/Ubuntu PHP-FPM unit, and the
// old rule ("contains a dot, so it already has a type") sent it to
// systemd's Manager bare, which the auditor saw systemd 258 refuse
// ("Unit name php8.2-fpm is not valid."). That refusal is the
// auditor's observation on a real machine; it is not reproduced here,
// where there is no systemd. What this pins is the name we send.
func TestWithServiceSuffix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// Bare names: `.service` is the default type.
		{"nginx", "nginx.service"},
		{"cron", "cron.service"},
		{"getty@tty1", "getty@tty1.service"},

		// Dots that are not a unit type.
		{"php8.2-fpm", "php8.2-fpm.service"},
		{"php7.4-fpm", "php7.4-fpm.service"},
		{"postgresql@16-main.x", "postgresql@16-main.x.service"},
		{"org.example.daemon", "org.example.daemon.service"},
		// A type name that is not the last component is not a suffix.
		{"foo.socket.d", "foo.socket.d.service"},
		// Salt matches with endswith and no dot, so "fooservice" would
		// count as typed there; a type is a dot-separated suffix here.
		{"fooservice", "fooservice.service"},

		// Every real unit type is left alone.
		{"nginx.service", "nginx.service"},
		{"php8.2-fpm.service", "php8.2-fpm.service"},
		{"foo.socket", "foo.socket"},
		{"dev-sda.device", "dev-sda.device"},
		{"home.mount", "home.mount"},
		{"proc-sys-fs-binfmt_misc.automount", "proc-sys-fs-binfmt_misc.automount"},
		{"dev-zram0.swap", "dev-zram0.swap"},
		{"multi-user.target", "multi-user.target"},
		{"foo.path", "foo.path"},
		{"bar.timer", "bar.timer"},
		{"user.slice", "user.slice"},
		{"session-1.scope", "session-1.scope"},
		{"getty@.service", "getty@.service"},
	} {
		if got := withServiceSuffix(tc.in); got != tc.want {
			t.Errorf("withServiceSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// With the bus up, every mutating verb and every read hands systemd the
// canonical name for a dotted unit. Before the fix all of these carried
// the bare `php8.2-fpm`.
func TestSystemdProviderDottedNameOverDBus(t *testing.T) {
	fake := &fakeSystemd{active: "active", fileState: "enabled"}
	useSystemd(t, fake)
	c, runner := serviceCtx(t, nil)
	p := systemdProvider{}
	const unit = "php8.2-fpm"

	for _, do := range []func() error{
		func() error { return p.Start(c, unit) },
		func() error { return p.Stop(c, unit) },
		func() error { return p.Restart(c, unit) },
		func() error { return p.Reload(c, unit) },
		func() error { return p.Enable(c, unit) },
		func() error { return p.Disable(c, unit) },
		func() error { return p.Mask(c, unit) },
		func() error { return p.Unmask(c, unit) },
		func() error { _, err := p.Status(c, unit); return err },
		func() error { _, err := p.Enabled(c, unit); return err },
		func() error { _, err := p.Masked(c, unit); return err },
	} {
		if err := do(); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{
		"StartUnit php8.2-fpm.service", "StopUnit php8.2-fpm.service",
		"RestartUnit php8.2-fpm.service", "ReloadUnit php8.2-fpm.service",
		"enable php8.2-fpm.service", "disable php8.2-fpm.service",
		"mask php8.2-fpm.service", "unmask php8.2-fpm.service",
		"activeState php8.2-fpm.service", "unitFileState php8.2-fpm.service",
		"unitFileState php8.2-fpm.service",
	}
	if strings.Join(fake.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("D-Bus calls:\n%s\nwant:\n%s", strings.Join(fake.calls, "\n"), strings.Join(want, "\n"))
	}
	if ran := runner.RanCommands(); len(ran) != 0 {
		t.Errorf("systemctl was shelled out to while the bus was up: %v", ran)
	}
}

// With the bus unreachable, the systemctl fallback is handed the very
// same name the D-Bus path would have been -- the two paths agree by
// construction, rather than by systemctl's own mangling happening to
// match ours. A typed name passes through untouched on both.
func TestSystemdProviderDottedNameFallbackAgrees(t *testing.T) {
	useSystemd(t, nil) // bus unreachable
	c, runner := serviceCtx(t, nil)
	p := systemdProvider{}

	for _, do := range []func() error{
		func() error { return p.Start(c, "php8.2-fpm") },
		func() error { return p.Stop(c, "php8.2-fpm") },
		func() error { return p.Restart(c, "php8.2-fpm") },
		func() error { return p.Reload(c, "php8.2-fpm") },
		func() error { return p.Enable(c, "php8.2-fpm") },
		func() error { return p.Disable(c, "php8.2-fpm") },
		func() error { return p.Mask(c, "php8.2-fpm") },
		func() error { return p.Unmask(c, "php8.2-fpm") },
		func() error { _, err := p.Status(c, "php8.2-fpm"); return err },
		func() error { _, err := p.Enabled(c, "php8.2-fpm"); return err },
		func() error { _, err := p.Masked(c, "php8.2-fpm"); return err },
		func() error { return p.Start(c, "foo.socket") },
		func() error { return p.Enable(c, "bar.timer") },
	} {
		if err := do(); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{
		"systemctl start php8.2-fpm.service",
		"systemctl stop php8.2-fpm.service",
		"systemctl restart php8.2-fpm.service",
		"systemctl reload php8.2-fpm.service",
		"systemctl enable php8.2-fpm.service",
		"systemctl disable php8.2-fpm.service",
		"systemctl mask php8.2-fpm.service",
		"systemctl unmask php8.2-fpm.service",
		"systemctl is-active --quiet php8.2-fpm.service",
		"systemctl is-enabled php8.2-fpm.service",
		"systemctl is-enabled php8.2-fpm.service",
		"systemctl start foo.socket",
		"systemctl enable bar.timer",
	}
	got := runner.RanCommands()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("systemctl argv:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Masked's fallback reads `systemctl is-enabled` for the same unit the
// D-Bus path asks about, and answers from what it prints. It was the
// one argv 5.309 left with the raw name; the response here is keyed on
// the canonical argv, so a raw name gets the runner's empty default and
// reads as not masked. The stdout is the word Masked already compares
// against, not captured systemctl output; none is in the repository.
func TestSystemdProviderMaskedFallbackUsesTheCanonicalName(t *testing.T) {
	useSystemd(t, nil) // bus unreachable
	c, runner := serviceCtx(t, map[string]exec.Result{
		"systemctl is-enabled php8.2-fpm.service": {Stdout: "masked\n"},
	})
	masked, err := systemdProvider{}.Masked(c, "php8.2-fpm")
	if err != nil {
		t.Fatal(err)
	}
	if !masked {
		t.Errorf("Masked(php8.2-fpm) = false; ran %q", runner.RanCommands())
	}
}

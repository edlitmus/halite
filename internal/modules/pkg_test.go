package modules

import "testing"

// fakeBackend is a package manager whose answers the test controls.
func fakeBackend(version string, held map[string]bool) *pkgBackend {
	return &pkgBackend{
		name:      "fake",
		installed: func(string) bool { return true },
		version:   func(string) string { return version },
		pin:       func(p, v string) string { return p + "=" + v },
		hold:      func(p string) error { held[p] = true; return nil },
		unhold:    func(p string) error { held[p] = false; return nil },
		held:      func(p string) bool { return held[p] },
	}
}

func TestVersionDrift(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		want      string
		drifted   bool
	}{
		{"no version asked for", "1.24.0", "", false},
		{"same version", "1.24.0", "1.24.0", false},
		{"other version", "1.22.1", "1.24.0", true},
		{"backend cannot say", "", "1.24.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be := fakeBackend(tc.installed, nil)
			if got := versionDrift(be, "nginx", tc.want); got != tc.drifted {
				t.Fatalf("want drift=%v, got %v", tc.drifted, got)
			}
		})
	}
}

func TestVersionPinningNeedsABackendThatCanPin(t *testing.T) {
	be := fakeBackend("1.22.1", nil)
	if err := pinnable(be, []string{"nginx"}, "1.24.0"); err != nil {
		t.Fatalf("a backend with pin support should accept a version: %v", err)
	}
	if err := pinnable(be, []string{"nginx", "curl"}, "1.24.0"); err == nil {
		t.Fatal("one version cannot apply to several packages")
	}
	be.pin = nil
	if err := pinnable(be, []string{"nginx"}, "1.24.0"); err == nil {
		t.Fatal("a backend that cannot pin should fail rather than install the current version")
	}
	if err := pinnable(be, []string{"nginx", "curl"}, ""); err != nil {
		t.Fatalf("no version asked for is always fine: %v", err)
	}
}

func TestHoldDrift(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		held    map[string]bool
		drifted []string
		want    bool
	}{
		{"not declared", map[string]any{}, map[string]bool{"nginx": true}, nil, false},
		{"hold a free package", map[string]any{"hold": "true"}, map[string]bool{}, []string{"nginx"}, true},
		{"already held", map[string]any{"hold": "true"}, map[string]bool{"nginx": true}, nil, true},
		{"release a hold", map[string]any{"hold": "false"}, map[string]bool{"nginx": true}, []string{"nginx"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drifted, want, err := holdDrift(fakeBackend("1.0", tc.held), tc.args, []string{"nginx"})
			if err != nil {
				t.Fatal(err)
			}
			if len(drifted) != len(tc.drifted) {
				t.Fatalf("want %v, got %v", tc.drifted, drifted)
			}
			if len(tc.drifted) > 0 && want != tc.want {
				t.Fatalf("want hold=%v, got %v", tc.want, want)
			}
		})
	}
}

func TestHoldsNeedABackendThatSupportsThem(t *testing.T) {
	be := fakeBackend("1.0", map[string]bool{})
	be.hold = nil
	if _, _, err := holdDrift(be, map[string]any{"hold": "true"}, []string{"nginx"}); err == nil {
		t.Fatal("a backend with no holds should say so rather than ignore the request")
	}
}

func TestPlanReadsAsOnePhrasePerAction(t *testing.T) {
	be := fakeBackend("1.0", nil)
	got := pkgPlan(be, []string{"nginx=1.24.0"}, []string{"nginx"}, true, true)
	if len(got) != 2 {
		t.Fatalf("want an install phrase and a hold phrase, got %v", got)
	}
	if got[0] != "would install via fake: nginx=1.24.0" {
		t.Fatalf("unexpected install phrase %q", got[0])
	}
	if got[1] != "would hold via fake: nginx" {
		t.Fatalf("unexpected hold phrase %q", got[1])
	}
	done := pkgPlan(be, []string{"nginx"}, []string{"nginx"}, false, false)
	if done[0] != "installed via fake: nginx" || done[1] != "released via fake: nginx" {
		t.Fatalf("past tense should read as past tense, got %v", done)
	}
}

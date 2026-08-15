package modules

import (
	"strings"
	"testing"
)

// fakeRuntime is a runtime whose image lookups fail, so the argv builder
// falls back to the reference and the tests stay off the network.
func fakeRuntime() *containerRuntime { return &containerRuntime{name: "halite-no-such-runtime"} }

func TestRunArgvCarriesEveryArgument(t *testing.T) {
	argv, hash, err := containerRunArgv(fakeRuntime(), "web", map[string]any{
		"image":    "docker.io/library/nginx:1.27",
		"ports":    []any{"8080:80", "8443:443"},
		"volumes":  []any{"/srv/site:/usr/share/nginx/html:ro"},
		"env":      map[string]any{"NGINX_HOST": "example.com", "TZ": "UTC"},
		"restart":  "always",
		"network":  "web",
		"user":     "www",
		"workdir":  "/usr/share/nginx/html",
		"command":  "nginx -g daemon off;",
		"run_args": []any{"--memory", "512m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(argv, " ")
	for _, want := range []string{
		"run --detach --name web",
		"--restart always",
		"--network web",
		"--user www",
		"--workdir /usr/share/nginx/html",
		"--publish 8080:80 --publish 8443:443",
		"--volume /srv/site:/usr/share/nginx/html:ro",
		"--env NGINX_HOST=example.com --env TZ=UTC", // sorted
		"--memory 512m",
		"--label " + specLabel + "=" + hash,
		"docker.io/library/nginx:1.27 nginx -g daemon off;",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("want %q in:\n%s", want, line)
		}
	}
	if strings.Index(line, "--label") > strings.Index(line, "nginx:1.27") {
		t.Fatal("flags belong before the image, or the runtime reads them as the command")
	}
}

func TestSpecHashIsStableAndSensitive(t *testing.T) {
	base := map[string]any{
		"image": "nginx:1.27",
		"env":   map[string]any{"A": "1", "B": "2"},
		"ports": []any{"8080:80"},
	}
	_, first, err := containerRunArgv(fakeRuntime(), "web", base)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, again, _ := containerRunArgv(fakeRuntime(), "web", base)
		if again != first {
			t.Fatal("the same arguments must hash the same, or every run recreates the container")
		}
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"image", func(m map[string]any) { m["image"] = "nginx:1.28" }},
		{"a port", func(m map[string]any) { m["ports"] = []any{"9090:80"} }},
		{"an environment value", func(m map[string]any) { m["env"] = map[string]any{"A": "9", "B": "2"} }},
		{"a command", func(m map[string]any) { m["command"] = "sleep 1" }},
		{"a volume", func(m map[string]any) { m["volumes"] = []any{"/a:/b"} }},
		{"the restart policy", func(m map[string]any) { m["restart"] = "always" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := map[string]any{}
			for k, v := range base {
				changed[k] = v
			}
			tc.mutate(changed)
			_, got, err := containerRunArgv(fakeRuntime(), "web", changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == first {
				t.Fatalf("changing %s has to change the hash, or the container is never recreated", tc.name)
			}
		})
	}
}

func TestEnvironmentTakesEitherSpelling(t *testing.T) {
	fromMap, _, err := containerRunArgv(fakeRuntime(), "web",
		map[string]any{"image": "x", "env": map[string]any{"B": "2", "A": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	fromList, _, err := containerRunArgv(fakeRuntime(), "web",
		map[string]any{"image": "x", "env": []any{"B=2", "A=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fromMap, " ") != strings.Join(fromList, " ") {
		t.Fatalf("a mapping and a list of KEY=VALUE should mean the same:\n%v\n%v", fromMap, fromList)
	}
}

func TestRunArgvNeedsAnImage(t *testing.T) {
	if _, _, err := containerRunArgv(fakeRuntime(), "web", map[string]any{}); err == nil {
		t.Fatal("a container with no image cannot be created")
	}
}

func TestContainerStateIsRead(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		running bool
		spec    string
	}{
		{"running with a spec", "true 0123456789abcdef\n", true, "0123456789abcdef"},
		{"stopped with a spec", "false 0123456789abcdef", false, "0123456789abcdef"},
		{"no label at all", "true <no value>", true, ""},
		{"nil label", "false <nil>", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseContainerState(tc.out)
			if !got.exists {
				t.Fatal("output at all means the container is there")
			}
			if got.running != tc.running || got.spec != tc.spec {
				t.Fatalf("want running=%v spec=%q, got %+v", tc.running, tc.spec, got)
			}
		})
	}
}

func TestRuntimeDetection(t *testing.T) {
	if _, err := detectContainerRuntime(map[string]any{"runtime": "halite-no-such-runtime"}); err == nil {
		t.Fatal("a named runtime that is not installed should be reported")
	}
	rt, err := detectContainerRuntime(nil)
	if err != nil {
		// No runtime on this host is a fine outcome; the message is what
		// an operator would need.
		if !strings.Contains(err.Error(), "docker or podman") {
			t.Fatalf("unhelpful error %v", err)
		}
		return
	}
	if rt.name != "docker" && rt.name != "podman" {
		t.Fatalf("unexpected runtime %q", rt.name)
	}
}

// TestRuntimeAcceptsTheArgvHaliteBuilds checks the command line against the
// runtime's own parser rather than against my idea of its flags.
//
// The image is one that cannot resolve, so the run always fails: flags are
// parsed first, then the image is looked up and is not there. That keeps
// the check non-destructive on a host where the runtime would otherwise
// have started a container — a test must not leave one behind. A *flag*
// error is a different failure from a missing image or a permission one,
// and only the first is halite's fault.
func TestRuntimeAcceptsTheArgvHaliteBuilds(t *testing.T) {
	rt, err := detectContainerRuntime(nil)
	if err != nil {
		t.Skip("no container runtime")
	}
	argv, _, err := containerRunArgv(rt, "halite-argv-check", map[string]any{
		"image":   "localhost/halite-argv-check-does-not-exist:testing",
		"ports":   []any{"18080:80"},
		"volumes": []any{"/tmp:/data:ro"},
		"env":     map[string]any{"K": "V"},
		"restart": "always",
		"user":    "nobody",
		"workdir": "/data",
		"command": "echo hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, _, _ := run(rt.name, argv...)
	combined := out + errOut
	if !strings.Contains(combined, "halite-argv-check-does-not-exist") &&
		!strings.Contains(combined, "rootless") && !strings.Contains(combined, "denied") {
		t.Fatalf("expected the run to fail on the image or on privileges, got:\n%s", combined)
	}
	for _, bad := range []string{"unknown flag", "unknown shorthand", "invalid argument", "requires at least"} {
		if strings.Contains(combined, bad) {
			t.Fatalf("%s rejected the command line (%s):\n%s %s", rt.name, bad, rt.name, strings.Join(argv, " "))
		}
	}
}

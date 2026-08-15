package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

func init() {
	register("container.image_present", containerImagePresent)
	register("container.image_absent", containerImageAbsent)
	register("container.running", containerRunning)
	register("container.stopped", containerStopped)
	register("container.absent", containerAbsent)
}

// specLabel carries the hash of the arguments a container was created
// from. Comparing it against what the state would create now is how drift
// is detected: every argument is covered by one comparison, including the
// ones a future version of this module adds.
const specLabel = "halite.spec"

// containerRuntime is the CLI that speaks the OCI verbs. docker and podman
// take the same subcommands for everything used here, so one backend
// drives both — the FreeBSD hosts halite targets have podman, and the
// Linux ones usually have docker.
type containerRuntime struct{ name string }

func detectContainerRuntime(args map[string]any) (*containerRuntime, error) {
	if explicit := Str(args, "runtime", ""); explicit != "" {
		if !has(explicit) {
			return nil, fmt.Errorf("runtime %q not found", explicit)
		}
		return &containerRuntime{name: explicit}, nil
	}
	for _, candidate := range []string{"docker", "podman"} {
		if has(candidate) {
			return &containerRuntime{name: candidate}, nil
		}
	}
	return nil, fmt.Errorf("no container runtime found (docker or podman)")
}

func (r *containerRuntime) query(argv ...string) (string, bool) {
	return pkgQuery(append([]string{r.name}, argv...)...)
}

func (r *containerRuntime) run(argv ...string) (string, error) {
	return pkgRun(append([]string{r.name}, argv...)...)
}

// containerImagePresent pulls an image if it is not already there.
//
//	docker.io/library/nginx:1.27:
//	  container.image_present
//
// `force: true` pulls even when the image is present, which is how a
// moving tag is refreshed.
func containerImagePresent(c *Ctx, id string, args map[string]any) Result {
	rt, err := detectContainerRuntime(args)
	if err != nil {
		return resFail("%v", err)
	}
	ref := Str(args, "name", id)
	force := Bool(args, "force", false)

	before := rt.imageID(ref)
	if before != "" && !force {
		return resOK(fmt.Sprintf("image %s is present", ref))
	}
	if c.Test {
		if before == "" {
			return resWould(fmt.Sprintf("image %s would be pulled", ref))
		}
		return resWould(fmt.Sprintf("image %s would be pulled again (force)", ref))
	}
	if out, err := rt.run("pull", ref); err != nil {
		return resFail("%s pull %s: %v: %s", rt.name, ref, err, strings.TrimSpace(out))
	}
	after := rt.imageID(ref)
	if before == after {
		return resOK(fmt.Sprintf("image %s is present (already current)", ref))
	}
	change := "pulled"
	if before != "" {
		change = shortID(before) + " -> " + shortID(after)
	}
	return resChanged(fmt.Sprintf("image %s %s", ref, change), map[string]string{ref: change})
}

// containerImageAbsent removes an image.
func containerImageAbsent(c *Ctx, id string, args map[string]any) Result {
	rt, err := detectContainerRuntime(args)
	if err != nil {
		return resFail("%v", err)
	}
	ref := Str(args, "name", id)
	if rt.imageID(ref) == "" {
		return resOK(fmt.Sprintf("image %s is absent", ref))
	}
	if c.Test {
		return resWould(fmt.Sprintf("image %s would be removed", ref))
	}
	if out, err := rt.run("rmi", ref); err != nil {
		return resFail("%s rmi %s: %v: %s", rt.name, ref, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("image %s removed", ref), map[string]string{ref: "removed"})
}

// containerRunning ensures a container exists, matches the state, and is
// up.
//
//	web:
//	  container.running:
//	    - image: docker.io/library/nginx:1.27
//	    - ports:
//	      - 8080:80
//	    - env:
//	        NGINX_HOST: example.com
//	    - restart: always
//
// A container whose arguments no longer match is replaced, not adjusted:
// the runtimes cannot change most of them on a live container, and a
// half-applied container would be worse than a recreated one.
func containerRunning(c *Ctx, id string, args map[string]any) Result {
	rt, err := detectContainerRuntime(args)
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	argv, hash, err := containerRunArgv(rt, name, args)
	if err != nil {
		return resFail("%v", err)
	}

	state := rt.containerState(name)
	restartWanted := Bool(args, "__watch_changed", false)

	switch {
	case !state.exists:
		if c.Test {
			return resWould(fmt.Sprintf("container %s would be created", name))
		}
		if out, err := rt.run(argv...); err != nil {
			return resFail("%s run %s: %v: %s", rt.name, name, err, strings.TrimSpace(out))
		}
		return resChanged(fmt.Sprintf("container %s created", name),
			map[string]string{name: "created"})

	case state.spec != hash:
		reason := "arguments changed"
		if state.spec == "" {
			reason = "not created by halite"
		}
		if c.Test {
			return resWould(fmt.Sprintf("container %s would be recreated (%s)", name, reason))
		}
		if err := rt.remove(name, state.running); err != nil {
			return resFail("%v", err)
		}
		if out, err := rt.run(argv...); err != nil {
			return resFail("%s run %s: %v: %s", rt.name, name, err, strings.TrimSpace(out))
		}
		return resChanged(fmt.Sprintf("container %s recreated (%s)", name, reason),
			map[string]string{name: "recreated: " + reason})

	case !state.running:
		if c.Test {
			return resWould(fmt.Sprintf("container %s would be started", name))
		}
		if out, err := rt.run("start", name); err != nil {
			return resFail("%s start %s: %v: %s", rt.name, name, err, strings.TrimSpace(out))
		}
		return resChanged(fmt.Sprintf("container %s started", name),
			map[string]string{name: "started"})

	case restartWanted:
		if c.Test {
			return resWould(fmt.Sprintf("container %s would be restarted (watch)", name))
		}
		if out, err := rt.run("restart", name); err != nil {
			return resFail("%s restart %s: %v: %s", rt.name, name, err, strings.TrimSpace(out))
		}
		return resChanged(fmt.Sprintf("container %s restarted", name),
			map[string]string{name: "restarted (watch)"})
	}
	return resOK(fmt.Sprintf("container %s is running", name))
}

// containerStopped ensures a container is not running. It stays where it
// is, so starting it again is `container.running`.
func containerStopped(c *Ctx, id string, args map[string]any) Result {
	rt, err := detectContainerRuntime(args)
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	state := rt.containerState(name)
	if !state.exists || !state.running {
		return resOK(fmt.Sprintf("container %s is not running", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("container %s would be stopped", name))
	}
	if out, err := rt.run("stop", name); err != nil {
		return resFail("%s stop %s: %v: %s", rt.name, name, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("container %s stopped", name), map[string]string{name: "stopped"})
}

// containerAbsent removes a container, stopping it first. Its volumes are
// left alone: a named volume outlives the container by design, and
// removing one is data loss.
func containerAbsent(c *Ctx, id string, args map[string]any) Result {
	rt, err := detectContainerRuntime(args)
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	state := rt.containerState(name)
	if !state.exists {
		return resOK(fmt.Sprintf("container %s is absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("container %s would be removed", name))
	}
	if err := rt.remove(name, state.running); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("container %s removed", name), map[string]string{name: "removed"})
}

// containerFacts is what the runtime says about a container.
type containerFacts struct {
	exists  bool
	running bool
	spec    string // the halite.spec label, empty when something else made it
}

// containerState asks for both facts in one call, since each one is a
// process.
func (r *containerRuntime) containerState(name string) containerFacts {
	out, ok := r.query("container", "inspect", name,
		"--format", "{{.State.Running}} {{index .Config.Labels \""+specLabel+"\"}}")
	if !ok {
		return containerFacts{}
	}
	return parseContainerState(out)
}

// parseContainerState reads the two fields the inspect format prints. A
// container without the label prints the runtime's own placeholder for a
// missing map entry, which is not a spec.
func parseContainerState(out string) containerFacts {
	fields := strings.Fields(strings.TrimSpace(out))
	facts := containerFacts{exists: true}
	if len(fields) > 0 {
		facts.running = fields[0] == "true"
	}
	if len(fields) > 1 && fields[1] != "<no" && fields[1] != "<nil>" {
		facts.spec = fields[1]
	}
	return facts
}

// imageID resolves an image reference to its id, or "" when the image is
// not present locally.
func (r *containerRuntime) imageID(ref string) string {
	out, ok := r.query("image", "inspect", ref, "--format", "{{.Id}}")
	if !ok {
		return ""
	}
	return strings.TrimSpace(out)
}

func (r *containerRuntime) remove(name string, running bool) error {
	if running {
		if out, err := r.run("stop", name); err != nil {
			return fmt.Errorf("%s stop %s: %w: %s", r.name, name, err, strings.TrimSpace(out))
		}
	}
	if out, err := r.run("rm", name); err != nil {
		return fmt.Errorf("%s rm %s: %w: %s", r.name, name, err, strings.TrimSpace(out))
	}
	return nil
}

// containerRunArgv builds the create command and the hash that identifies
// it. The hash covers the whole command line, so an argument this module
// grows later is compared without anybody remembering to compare it.
//
// The resolved image id is part of the hash where it can be read, so a tag
// that moved recreates the container. Where it cannot — the image is not
// pulled yet — the reference stands in, and the recreate happens on the
// run after the pull.
func containerRunArgv(rt *containerRuntime, name string, args map[string]any) ([]string, string, error) {
	image := Str(args, "image", "")
	if image == "" {
		return nil, "", fmt.Errorf("container.running needs an image")
	}
	argv := []string{"run", "--detach", "--name", name}

	if policy := Str(args, "restart", ""); policy != "" {
		argv = append(argv, "--restart", policy)
	}
	if network := Str(args, "network", ""); network != "" {
		argv = append(argv, "--network", network)
	}
	if user := Str(args, "user", ""); user != "" {
		argv = append(argv, "--user", user)
	}
	if workdir := Str(args, "workdir", ""); workdir != "" {
		argv = append(argv, "--workdir", workdir)
	}
	for _, port := range List(args, "ports") {
		argv = append(argv, "--publish", port)
	}
	for _, volume := range List(args, "volumes") {
		argv = append(argv, "--volume", volume)
	}
	env, err := containerPairs(args, "env")
	if err != nil {
		return nil, "", err
	}
	for _, pair := range env {
		argv = append(argv, "--env", pair)
	}
	labels, err := containerPairs(args, "labels")
	if err != nil {
		return nil, "", err
	}
	for _, pair := range labels {
		argv = append(argv, "--label", pair)
	}
	argv = append(argv, List(args, "run_args")...)

	// The hash is taken before the spec label is added, so that adding the
	// label cannot change the thing it identifies.
	spec := append([]string{}, argv...)
	spec = append(spec, imageIdentity(rt, image))
	if command := Str(args, "command", ""); command != "" {
		spec = append(spec, strings.Fields(command)...)
	}
	hash := specHash(spec)

	argv = append(argv, "--label", specLabel+"="+hash, image)
	if command := Str(args, "command", ""); command != "" {
		argv = append(argv, strings.Fields(command)...)
	}
	return argv, hash, nil
}

// imageIdentity is the image id when it can be read, and the reference
// otherwise.
func imageIdentity(rt *containerRuntime, image string) string {
	if id := rt.imageID(image); id != "" {
		return id
	}
	return image
}

// containerPairs reads a mapping argument as sorted KEY=VALUE strings, or
// takes a list of them as written.
func containerPairs(args map[string]any, key string) ([]string, error) {
	mapping, declared := Map(args, key)
	if !declared {
		return nil, nil
	}
	if mapping == nil {
		// A list of "KEY=VALUE" is the other spelling, and the one a
		// docker-compose habit produces.
		if list := List(args, key); len(list) > 0 {
			sorted := append([]string{}, list...)
			sort.Strings(sorted)
			return sorted, nil
		}
		return nil, fmt.Errorf("%s must be a mapping or a list of KEY=VALUE", key)
	}
	pairs := make([]string, 0, len(mapping))
	for name, value := range mapping {
		pairs = append(pairs, fmt.Sprintf("%s=%v", name, value))
	}
	sort.Strings(pairs)
	return pairs, nil
}

// specHash identifies a command line. It is a fingerprint for comparison,
// not a secret.
func specHash(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])[:32]
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

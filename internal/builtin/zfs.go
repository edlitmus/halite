package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerZFS installs the zfs module. The zpool module is beside it in
// zpool.go.
//
// Both tools have a scripted output mode — `-H -p` for zfs, `-H -p` for
// zpool — which emits tab-separated fields with no header and no unit
// suffixes. Parsing that is stable; parsing the human output is not, and
// a configuration management system that misreads a pool's health is
// worse than one that cannot read it at all.
func registerZFS(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "list",
				Doc: "Return the datasets and their space usage.",
				Params: []signature.Param{
					opt("dataset", signature.String, "", "Restrict to one dataset and its children."),
					opt("recursive", signature.Bool, false, "Include children."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"zfs", "list", "-H", "-p", "-o", "name,used,avail,refer,mountpoint"}
				if states.Bool(args, "recursive", false) {
					argv = append(argv, "-r")
				}
				if d := states.Str(args, "dataset", ""); d != "" {
					argv = append(argv, d)
				}
				res, err := zfsRun(c, argv...)
				if err != nil {
					return nil, err
				}
				out := value.NewMap(16)
				for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
					f := strings.Split(line, "\t")
					if len(f) < 5 {
						continue
					}
					out.Set(f[0], value.MapOf(
						"used", parseInt64(f[1]),
						"available", parseInt64(f[2]),
						"referenced", parseInt64(f[3]),
						"mountpoint", f[4],
					))
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "get",
				Doc: "Return a dataset's properties.",
				Params: []signature.Param{
					req("dataset", signature.String, "The dataset."),
					opt("properties", signature.List, nil, "Which properties; defaults to all."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				props := "all"
				if p := states.Strings(args, "properties"); len(p) > 0 {
					props = strings.Join(p, ",")
				}
				res, err := zfsRun(c, "zfs", "get", "-H", "-p", "-o", "property,value", props,
					states.Str(args, "dataset", ""))
				if err != nil {
					return nil, err
				}
				return parseTabPairs(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "exists",
				Doc:       "Report whether a dataset exists.",
				Params:    []signature.Param{req("dataset", signature.String, "The dataset.")},
				TestMode:  signature.TestNotApplicable,
				Platforms: []string{"freebsd", "linux", "illumos"},
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return zfsExists(c, states.Str(args, "dataset", "")), nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "filesystem_present",
				Doc: "Ensure a ZFS filesystem exists with the given properties.",
				Params: []signature.Param{
					nameParam("The dataset. Defaults to the state ID."),
					opt("properties", signature.Map, nil, "Properties to set."),
					opt("create_parent", signature.Bool, false, "Create the parent datasets."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root, or a delegated zfs permission"},
				Platforms:  []string{"freebsd", "linux", "illumos"},
				Section:    "15.5",
			},
			Fn: zfsFilesystemPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "zfs", Function: "absent",
				Doc: "Ensure a ZFS dataset does not exist.",
				Params: []signature.Param{
					nameParam("The dataset. Defaults to the state ID."),
					opt("recursive", signature.Bool, false, "Destroy the children too."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root, or a delegated zfs permission"},
				Platforms:  []string{"freebsd", "linux", "illumos"},
				Section:    "15.5",
			},
			Fn: zfsAbsent,
		},
	)
}

func zfsRun(c *exec.Context, argv ...string) (exec.Result, error) {
	if c.Which(argv[0]) == "" {
		return exec.Result{}, fmt.Errorf("%s was not found on this node", argv[0])
	}
	return c.Run(exec.Command{Argv: argv})
}

func zfsExists(c *exec.Context, dataset string) bool {
	if c.Which("zfs") == "" {
		return false
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"zfs", "list", "-H", "-o", "name", dataset},
		IgnoreExitCode: true,
	})
	return err == nil && res.Code == 0
}

// parseTabPairs reads `key\tvalue` lines into a mapping.
func parseTabPairs(out string) *value.Map {
	m := value.NewMap(32)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		m.Set(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return m
}

func zfsFilesystemPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	dataset := states.Str(args, "name", "")
	if dataset == "" {
		return states.False("This state needs a dataset name."), nil
	}
	if c.Which("zfs") == "" {
		return states.False("zfs was not found on this node."), nil
	}
	props := states.Mapping(args, "properties")

	exists := zfsExists(c, dataset)
	changes := value.NewMap(4)

	if !exists {
		changes.Set(dataset, states.Change(nil, "present"))
	} else if props != nil {
		current, err := zfsRun(c, "zfs", "get", "-H", "-p", "-o", "property,value", propertyList(props), dataset)
		if err != nil {
			return states.False(fmt.Sprintf("The properties of %s could not be read: %v", dataset, err)), nil
		}
		have := parseTabPairs(current.Stdout)
		for _, e := range props.Entries() {
			name := value.KeyString(e.Key)
			want := value.KeyString(e.Val)
			if cur, ok := have.Get(name); !ok || value.KeyString(cur) != want {
				changes.Set(name, states.Change(cur, want))
			}
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The dataset %s already exists with the requested properties.", dataset)), nil
	}
	verb := "updated"
	if !exists {
		verb = "created"
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The dataset %s would be %s.", dataset, verb), changes), nil
	}

	if !exists {
		argv := []string{"zfs", "create"}
		if states.Bool(args, "create_parent", false) {
			argv = append(argv, "-p")
		}
		for _, name := range sortedPropertyNames(props) {
			v, _ := props.Get(name)
			argv = append(argv, "-o", name+"="+value.KeyString(v))
		}
		argv = append(argv, dataset)
		if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
			return states.False(fmt.Sprintf("The dataset %s could not be created: %v", dataset, err)), nil
		}
		return states.Changed(fmt.Sprintf("The dataset %s was created.", dataset), changes), nil
	}

	for _, e := range changes.Entries() {
		name := value.KeyString(e.Key)
		v, ok := props.Get(name)
		if !ok {
			continue
		}
		if _, err := c.Run(exec.Command{Argv: []string{"zfs", "set", name + "=" + value.KeyString(v), dataset}}); err != nil {
			return states.False(fmt.Sprintf("The property %s of %s could not be set: %v", name, dataset, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The dataset %s was updated.", dataset), changes), nil
}

func propertyList(props *value.Map) string {
	if props == nil || props.Len() == 0 {
		return "all"
	}
	return strings.Join(sortedPropertyNames(props), ",")
}

func sortedPropertyNames(props *value.Map) []string {
	if props == nil {
		return nil
	}
	out := props.StringKeys()
	sort.Strings(out)
	return out
}

func zfsAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	dataset := states.Str(args, "name", "")
	if c.Which("zfs") == "" {
		return states.False("zfs was not found on this node."), nil
	}
	if !zfsExists(c, dataset) {
		return states.True(fmt.Sprintf("The dataset %s is already absent.", dataset)), nil
	}
	changes := value.MapOf(dataset, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The dataset %s would be destroyed.", dataset), changes), nil
	}
	argv := []string{"zfs", "destroy"}
	if states.Bool(args, "recursive", false) {
		argv = append(argv, "-r")
	}
	argv = append(argv, dataset)
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return states.False(fmt.Sprintf("The dataset %s could not be destroyed: %v", dataset, err)), nil
	}
	return states.Changed(fmt.Sprintf("The dataset %s was destroyed.", dataset), changes), nil
}

package builtin

import (
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// win_registry, SPEC section 15.3.
//
// SPEC 15.5 does not name a registry state, so none ships: a tree that
// needs one reaches these through `module.run`. That is a decision the
// specification made and not one to reverse in passing — Salt has
// `reg.present` and an estate migrating from it will want the same, so
// it is worth a person's attention rather than a quiet addition here.
//
// The module goes through the registry API rather than running reg.exe.
// There is no argument for the binary here that there was for a package
// manager: reg.exe writes a table for a person, its type names and its
// error text are localised, and a value containing a newline cannot be
// told from two values in what it prints.

func registerWinRegistry(r *Registries) {
	hive := req("hive", signature.String,
		"The root: HKLM, HKCU, HKCR, HKU or HKCC, in either spelling.")
	key := req("key", signature.String, `The key path under the hive, as in SOFTWARE\Vendor\App.`)
	view := opt("view", signature.String, "native",
		"Which registry a 64-bit Windows keeps: native, 32 or 64. "+
			"A 32-bit application's settings live under the 32 view.")
	vname := opt("vname", signature.String, "",
		"The value's name. Empty is the key's default value, which regedit shows as (Default).")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "read_value",
				Doc:    "Read one registry value, with its type.",
				Params: []signature.Param{hive, key, vname, view},
				Returns: "a mapping with name, type and data; data is a string, " +
					"a list of strings, a number, or lowercase hex for binary",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winRegReadValue(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "vname", ""), states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "set_value",
				Doc: "Write one registry value, creating the key if it is not there.",
				Params: []signature.Param{
					hive, key, vname,
					req("vdata", signature.Any, "The value to write."),
					choice("vtype", "sz", "The registry type. It is stated rather than "+
						"guessed: a program reading a setting as a number does not find "+
						"one written as a string.",
						"sz", "expand_sz", "multi_sz", "dword", "qword", "binary"),
					view,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				data, _ := args.Get("vdata")
				err := winRegSetValue(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "vname", ""), states.Str(args, "vtype", "sz"),
					data, states.Str(args, "view", "native"))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "delete_value",
				Doc:        "Remove one registry value.",
				Params:     []signature.Param{hive, key, vname, view},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winRegDeleteValue(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "vname", ""), states.Str(args, "view", "native"))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "list_keys",
				Doc:       "Name the subkeys of a key, sorted.",
				Params:    []signature.Param{hive, key, view},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winRegListKeys(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "list_values",
				Doc:       "Read every value in a key, sorted by name, with its type and data.",
				Params:    []signature.Param{hive, key, view},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winRegListValues(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "key_exists",
				Doc:       "Report whether a key is there.",
				Params:    []signature.Param{hive, key, view},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winRegKeyExists(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "value_exists",
				Doc:       "Report whether a value is there.",
				Params:    []signature.Param{hive, key, vname, view},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winRegValueExists(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "vname", ""), states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "create_key",
				Doc:        "Create a key and every key above it. Reports whether it had to.",
				Params:     []signature.Param{hive, key, view},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return winRegCreateKey(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Str(args, "view", "native"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_registry", Function: "delete_key",
				Doc: "Remove a key. Refuses one that has subkeys unless `recursive` says otherwise, " +
					"because a state that names the wrong key and takes a subtree with it " +
					"is not recoverable from the state file.",
				Params: []signature.Param{
					hive, key,
					opt("recursive", signature.Bool, false, "Remove the subkeys too."),
					view,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winRegDeleteKey(
					states.Str(args, "hive", ""), states.Str(args, "key", ""),
					states.Bool(args, "recursive", false), states.Str(args, "view", "native"))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)
}

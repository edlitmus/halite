package migrate

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const nginxModule = `# -*- coding: utf-8 -*-
"""Custom nginx helpers."""
import logging

__virtualname__ = 'nginxinfo'


def __virtual__():
    return __virtualname__


def _parse(output):
    """Private."""
    return output


def version():
    """
    Return the installed nginx version.
    """
    return _parse('x')


def vhosts(path='/etc/nginx/sites-enabled',
           include_disabled=False,
           *extra,
           **kwargs):
    '''Return the configured virtual hosts.'''
    return []


def reload_if_changed(config, timeout=30, signal="HUP", tags=['a', 'b']):
    """Reload nginx when the config changed."""
    return {}
`

func TestReadPyModuleFindsWhatSaltWouldExpose(t *testing.T) {
	module := ReadPyModule("_modules/nginx_info.py", []byte(nginxModule))

	// `__virtualname__` is what a state calls it by, so it is what the
	// bridge must answer to — not the filename.
	if module.Name != "nginxinfo" {
		t.Errorf("the module is named %q", module.Name)
	}
	if !module.HasVirtual {
		t.Error("__virtual__ was not noticed")
	}
	names := make([]string, 0, len(module.Functions))
	for _, fn := range module.Functions {
		names = append(names, fn.Name)
	}
	want := "version, vhosts, reload_if_changed"
	if strings.Join(names, ", ") != want {
		t.Errorf("it found %s, want %s", strings.Join(names, ", "), want)
	}
}

// A signature spanning several lines, with a list default and a
// docstring in either quote style.
func TestPythonSignaturesAreReadInFull(t *testing.T) {
	module := ReadPyModule("m.py", []byte(nginxModule))
	byName := map[string]PyFunction{}
	for _, fn := range module.Functions {
		byName[fn.Name] = fn
	}

	vhosts := byName["vhosts"]
	if len(vhosts.Params) != 4 {
		t.Fatalf("vhosts has %d params: %+v", len(vhosts.Params), vhosts.Params)
	}
	if vhosts.Params[0].Name != "path" || vhosts.Params[0].Default != "'/etc/nginx/sites-enabled'" {
		t.Errorf("the first param is %+v", vhosts.Params[0])
	}
	if !vhosts.Params[2].Variadic || vhosts.Params[2].Name != "extra" {
		t.Errorf("*extra read as %+v", vhosts.Params[2])
	}
	if !vhosts.Params[3].Keywords || vhosts.Params[3].Name != "kwargs" {
		t.Errorf("**kwargs read as %+v", vhosts.Params[3])
	}
	if vhosts.Doc != "Return the configured virtual hosts." {
		t.Errorf("the docstring is %q", vhosts.Doc)
	}

	// A list default holds commas, which must not split the parameter.
	reload := byName["reload_if_changed"]
	if len(reload.Params) != 4 {
		t.Fatalf("reload_if_changed has %d params: %+v", len(reload.Params), reload.Params)
	}
	if reload.Params[3].Default != "['a', 'b']" {
		t.Errorf("the list default read as %q", reload.Params[3].Default)
	}
	// The first parameter has no default, which is not the same as
	// having an empty one.
	if reload.Params[0].HasDefault {
		t.Errorf("a required parameter reads as optional: %+v", reload.Params[0])
	}
	// A multi-line docstring gives its first line.
	if byName["version"].Doc != "Return the installed nginx version." {
		t.Errorf("the docstring is %q", byName["version"].Doc)
	}
}

// The generated skeleton has to compile. A generator that emits code
// which does not is worse than none: it turns "port this formula" into
// "port this formula and debug the tool".
func TestAGeneratedSkeletonParsesAsGo(t *testing.T) {
	module := ReadPyModule("_modules/nginx_info.py", []byte(nginxModule))
	skeleton := GenerateSkeleton(module, "module")

	if _, err := parser.ParseFile(token.NewFileSet(), skeleton.Path,
		skeleton.Source, parser.AllErrors); err != nil {
		t.Fatalf("the generated skeleton does not parse: %v\n%s", err, skeleton.Source)
	}
	if skeleton.Path != "halite-ext-nginxinfo/main.go" {
		t.Errorf("it goes to %s", skeleton.Path)
	}

	// Every function is a stub that fails, so a bridge generated and
	// forgotten fails loudly rather than answering nothing.
	for _, fn := range module.Functions {
		want := fn.Name + " is not written yet"
		if !strings.Contains(skeleton.Source, want) {
			t.Errorf("the skeleton does not stub %s", fn.Name)
		}
	}
	// And the parameters are there for whoever writes the body.
	for _, want := range []string{
		"path = '/etc/nginx/sites-enabled'",
		"config (required)",
		"**kwargs",
		"_modules/nginx_info.py:",
	} {
		if !strings.Contains(skeleton.Source, want) {
			t.Errorf("the skeleton is missing %q", want)
		}
	}
	// `__virtual__` cannot be inferred, and the skeleton says so
	// rather than dropping it.
	if !strings.Contains(skeleton.Source, "__virtual__") {
		t.Error("the skeleton says nothing about __virtual__")
	}
}

// A quote or a backtick in a docstring must not break the generated
// Go: a signature is emitted inside a raw string literal.
func TestADocstringCannotBreakTheGeneratedSource(t *testing.T) {
	src := "def odd(a):\n" +
		"    \"\"\"A doc with a ` backtick and a \" quote.\"\"\"\n" +
		"    return a\n"
	module := ReadPyModule("m.py", []byte(src))
	skeleton := GenerateSkeleton(module, "module")

	if _, err := parser.ParseFile(token.NewFileSet(), "main.go",
		skeleton.Source, parser.AllErrors); err != nil {
		t.Fatalf("a docstring broke the generated Go: %v\n%s", err, skeleton.Source)
	}
}

// `_utils` is a Python import target rather than an extension point,
// and a skeleton for it would be a file nobody could use.
func TestOnlyRealExtensionPointsGetASkeleton(t *testing.T) {
	for dir, want := range map[string]string{
		"_modules":   "module",
		"_returners": "returner",
		"_states":    "state",
	} {
		got, ok := KindForDir(dir)
		if !ok || got != want {
			t.Errorf("%s mapped to %q %v", dir, got, ok)
		}
	}
	for _, dir := range []string{"_utils", "_engines", "_thorium"} {
		if kind, ok := KindForDir(dir); ok {
			t.Errorf("%s mapped to %q, and it is not an extension point", dir, kind)
		}
	}
	// And Skeletons generates nothing for a module with no kind.
	module := ReadPyModule("_utils/helpers.py", []byte("def shared(a):\n    return a\n"))
	if got := Skeletons([]PyModule{module}, map[string]string{}); len(got) != 0 {
		t.Errorf("it generated %d skeletons for _utils", len(got))
	}
}

// A module name that is not a usable directory name still produces one.
func TestTheCommandNameIsAlwaysUsable(t *testing.T) {
	cases := map[string]string{
		"nginxinfo":  "halite-ext-nginxinfo",
		"my.module":  "halite-ext-my-module",
		"Weird Name": "halite-ext-weird-name",
		"":           "halite-ext-extension",
		"---":        "halite-ext-extension",
	}
	for in, want := range cases {
		if got := bridgeCommandName(in); got != want {
			t.Errorf("bridgeCommandName(%q) = %q, want %q", in, got, want)
		}
	}
}

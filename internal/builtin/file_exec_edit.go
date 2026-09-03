package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerFileEditExec exposes as execution functions the six file edits
// that were reachable only from an SLS.
//
// Salt has all six in `salt.modules.file`, so `salt['file.line'](…)` in
// a template and `halite-node call file.comment` both work there and
// failed here. Note that `file.managed`, `file.absent` and
// `file.directory` are *not* among them: they are states in Salt too,
// and a report that called them missing execution functions was wrong
// about what Salt does.
//
// Each is the state implementation with its result read as a value,
// because the work is the same work and two implementations of an edit
// are two chances to disagree about what it does. What differs is the
// answer: a state reports whether it changed something, and an execution
// function is asked to do it and say whether it worked.
func registerFileEditAsExec(r *Registries) {
	// Salt's execution functions name their first argument `path`, and
	// the states here name it `name`; `file.symlink` reverses the pair
	// outright, taking the target first and the link second. The
	// execution function has to take the names a tree already writes, so
	// the arguments are translated on the way in rather than the states
	// being renamed.
	edit := func(fn string, doc string, params []signature.Param, rename map[string]string,
		run func(*exec.Context, *value.Map) (states.Result, error)) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: fn,
				Doc:      doc,
				Params:   params,
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return asExecResult(run(c, renameArgs(args, rename)))
			},
		}
	}
	toName := map[string]string{"path": "name"}

	r.Exec.Add(
		edit("touch", "Create a file if it does not exist, and update its times if it does.",
			[]signature.Param{pathParam("The file to touch.")}, nil, fileTouch),

		edit("symlink", "Create a symbolic link.",
			[]signature.Param{
				req("src", signature.Path, "What the link points at."),
				req("path", signature.Path, "Where the link lives."),
				opt("force", signature.Bool, false, "Replace whatever is already there."),
			}, map[string]string{"path": "name", "src": "target"}, fileSymlink),

		edit("line", "Add, replace, or remove one line in a file.",
			[]signature.Param{
				req("path", signature.Path, "The file to edit."),
				opt("content", signature.String, "", "The line."),
				opt("match", signature.String, "", "The line to act on; defaults to `content`."),
				opt("mode", signature.String, "", "ensure, replace, delete, insert, or prepend."),
				opt("location", signature.String, "", "start or end, for insert."),
				opt("before", signature.String, "", "Insert before the line matching this."),
				opt("after", signature.String, "", "Insert after the line matching this."),
			}, toName, fileLine),

		edit("blockreplace", "Replace the text between two markers.",
			[]signature.Param{
				req("path", signature.Path, "The file to edit."),
				opt("marker_start", signature.String, "", "The line that opens the block."),
				opt("marker_end", signature.String, "", "The line that closes it."),
				opt("content", signature.String, "", "What goes between them."),
				opt("append_if_not_found", signature.Bool, false,
					"Add the block at the end when the markers are absent."),
				opt("prepend_if_not_found", signature.Bool, false,
					"Add the block at the start when the markers are absent."),
			}, toName, fileBlockReplace),

		edit("comment", "Comment out the lines matching a pattern.",
			[]signature.Param{
				req("path", signature.Path, "The file to edit."),
				req("regex", signature.String, "The pattern to match."),
				opt("char", signature.String, "#", "The comment character."),
			}, toName, func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileComment(c, args, true)
			}),

		edit("uncomment", "Uncomment the lines matching a pattern.",
			[]signature.Param{
				req("path", signature.Path, "The file to edit."),
				req("regex", signature.String, "The pattern to match."),
				opt("char", signature.String, "#", "The comment character."),
			}, toName, func(c *exec.Context, args *value.Map) (states.Result, error) {
				return fileComment(c, args, false)
			}),
	)
}

// asExecResult reads a state result as an execution function's answer.
//
// True when the edit was made or was already true, and an error when it
// failed — because an execution function that returns false for both
// "done" and "refused" makes the caller guess, and a template calling
// one has no other channel.
func asExecResult(res states.Result, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if !res.Succeeded() {
		return nil, fmt.Errorf("%s", res.Comment)
	}
	return true, nil
}

// renameArgs maps an execution function's argument names onto the ones
// the state implementation reads. A nil table means they already agree.
func renameArgs(args *value.Map, rename map[string]string) *value.Map {
	if len(rename) == 0 {
		return args
	}
	out := value.NewMap(args.Len())
	for _, e := range args.Entries() {
		key := value.KeyString(e.Key)
		if to, ok := rename[key]; ok {
			key = to
		}
		out.Set(key, e.Val)
	}
	return out
}

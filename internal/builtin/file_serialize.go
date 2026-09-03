package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// registerFileSerialize adds `file.serialize`, which writes a data
// structure to a file in a named format.
//
// Two references in a real estate's tree. It is how a tree turns pillar
// into a configuration file without a template — the data is already
// structured, and rendering it through Jinja to get JSON back is a
// round trip through text that can only lose.
func registerFileSerialize(r *Registries) {
	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "serialize",
			Doc: "Write a data structure to a file as JSON or YAML.",
			Params: []signature.Param{
				nameParam("The file to write. Defaults to the state ID."),
				opt("dataset", signature.Any, nil, "The data to write."),
				opt("dataset_pillar", signature.String, "",
					"A pillar key holding the data, instead of `dataset`."),
				opt("serializer", signature.String, "",
					"json or yaml. Required, as it is in Salt: the format is not "+
						"guessed from the file name."),
				opt("formatter", signature.String, "",
					"Salt's older name for `serializer`."),
				opt("merge_if_exists", signature.Bool, false,
					"Merge over what the file already holds rather than replacing it."),
				opt("mode", signature.String, "", "The file mode."),
				opt("user", signature.String, "", "The owner."),
				opt("group", signature.String, "", "The group."),
				opt("makedirs", signature.Bool, false, "Create the parent directory."),
				opt("create", signature.Bool, true,
					"Write the file when it does not exist. False updates only what is there."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileSerialize,
	})
}

func fileSerialize(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("file.serialize needs a file to write."), nil
	}

	format := states.Str(args, "serializer", "")
	if format == "" {
		format = states.Str(args, "formatter", "")
	}
	format = strings.ToLower(format)
	switch format {
	case "json", "yaml":
	case "":
		return states.False("file.serialize needs `serializer`: json or yaml. " +
			"The format is not guessed from the file name, because a `.conf` " +
			"that has always held YAML would start holding JSON the day " +
			"somebody renamed it."), nil
	default:
		return states.False(fmt.Sprintf(
			"%q is not a serializer this build has; use json or yaml.", format)), nil
	}

	dataset, err := serializeDataset(c, args)
	if err != nil {
		return states.False(err.Error()), nil
	}

	_, statErr := os.Stat(path)
	exists := statErr == nil
	if !exists && !states.Bool(args, "create", true) {
		return states.True(fmt.Sprintf(
			"%s does not exist and `create` is false, so nothing was written.", path)), nil
	}

	if states.Bool(args, "merge_if_exists", false) && exists {
		dataset, err = mergeWithFile(path, format, dataset)
		if err != nil {
			return states.False(err.Error()), nil
		}
	}

	want, err := encodeDataset(dataset, format)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be written as %s: %v", path, format, err)), nil
	}

	have, readErr := os.ReadFile(path)
	if readErr == nil && string(have) == string(want) {
		return states.True(fmt.Sprintf("%s already holds this data.", path)), nil
	}

	changes := value.MapOf("diff", states.Change(serializeWas(exists), "written"))
	if c.Test {
		verb := "would be written"
		if exists {
			verb = "would be rewritten"
		}
		return states.WouldChange(fmt.Sprintf("%s %s as %s.", path, verb, format), changes), nil
	}

	if states.Bool(args, "makedirs", false) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return states.False(fmt.Sprintf("%s could not be created: %v",
				filepath.Dir(path), err)), nil
		}
	}
	mode := modeOrDefault(states.Str(args, "mode", ""), 0o644)
	if err := writeAtomic(path, want, mode); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
	}
	if err := applyOwnership(path, states.Str(args, "user", ""),
		states.Str(args, "group", "")); err != nil {
		return states.False(fmt.Sprintf("%s was written and its ownership could not "+
			"be set: %v", path, err)), nil
	}

	verb := "written"
	if exists {
		verb = "rewritten"
	}
	return states.Changed(fmt.Sprintf("%s was %s as %s.", path, verb, format), changes), nil
}

func serializeWas(exists bool) string {
	if exists {
		return "different"
	}
	return "absent"
}

// serializeDataset reads the data to write, from the state or from
// pillar.
func serializeDataset(c *exec.Context, args *value.Map) (any, error) {
	fromPillar := states.Str(args, "dataset_pillar", "")
	dataset, hasDataset := args.Get("dataset")

	switch {
	case fromPillar != "" && hasDataset:
		return nil, fmt.Errorf("file.serialize takes `dataset` or `dataset_pillar`, " +
			"not both: two sources for one file is a question about which wins")
	case fromPillar != "":
		pillar, err := c.PillarOrErr()
		if err != nil {
			return nil, fmt.Errorf("the pillar this file is built from did not "+
				"compile, so it is not written: %w", err)
		}
		v, ok := lookupGrain(pillar, strings.Split(fromPillar, ":"))
		if !ok {
			return nil, fmt.Errorf("the pillar has no %s, so there is nothing to "+
				"write. An absent key is not an empty file", fromPillar)
		}
		return v, nil
	case hasDataset:
		return dataset, nil
	}
	return nil, fmt.Errorf("file.serialize needs `dataset` or `dataset_pillar`")
}

// encodeDataset renders the data in the requested format, ending with a
// newline as every other file this build writes does.
func encodeDataset(dataset any, format string) ([]byte, error) {
	if format == "json" {
		out, err := value.EncodeJSON(dataset, 2)
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	}
	return []byte(yaml.Encode(dataset, yaml.EncodeOptions{Indent: 2})), nil
}

// mergeWithFile reads what the file holds and merges the dataset over
// it, which is what `merge_if_exists` asks for.
//
// A file that does not parse is an error rather than something to
// overwrite: merging asks to keep what is there, and the one case where
// that cannot be honoured is the one where discarding it would be worst.
func mergeWithFile(path, format string, dataset any) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read to merge into: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return dataset, nil
	}

	var existing any
	if format == "json" {
		existing, err = value.DecodeJSON(raw)
	} else {
		existing, _, err = yaml.Parse(raw, yaml.DefaultOptions(path))
	}
	if err != nil {
		return nil, fmt.Errorf("%s does not parse as %s, so `merge_if_exists` cannot "+
			"keep what it holds: %w", path, format, err)
	}

	base, baseOK := existing.(*value.Map)
	over, overOK := dataset.(*value.Map)
	if !baseOK || !overOK {
		// Merging is a mapping operation. Anything else replaces, which
		// is what a list or a scalar can mean.
		return dataset, nil
	}
	merged := value.NewMap(base.Len() + over.Len())
	for _, e := range base.Entries() {
		merged.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	for _, e := range over.Entries() {
		merged.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
	return merged, nil
}

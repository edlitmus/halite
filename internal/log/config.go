package log

import (
	"fmt"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/redact"
)

// Overrides are the command line's say over the configuration file: the
// values of `--log-level` and `--log-fmt`, empty when they were not
// given.
//
// The binaries read the flags themselves and hand the values in, rather
// than this package reading a *cli.Args. Each binary's flag audit
// (TestEveryFlagIsDocumentedAndParsed) reads only that binary's own
// source for `args.Flag("…"`, and a flag parsed in here would look to
// all three like a documented flag nothing parses. The flags are the
// binary's surface; the setting names, which are what went wrong, are
// this function's.
type Overrides struct {
	Level  string
	Format string
}

// FromConfig builds a service's logger from SPEC 26.1's settings, by the
// names those settings have, with the command line taking precedence
// over the file.
//
// It is here, once, because it used to be three copies and the copies
// disagreed. The hub's first cut read `log_fmt`, which is not a setting,
// and was corrected; the node's was written from the corrected hub; the
// api kept the first cut, read `log_fmt` and passed no file, so an
// api.yaml with `log_format: console` and a `log_file` logged JSON to
// stderr and created nothing. Nothing said so -- the loader warns about a
// key in the file it does not know, not about a declared key the program
// never asks for, and `log_format` was read by the other two, so the
// unread-key audit was satisfied. A pair of paths that must agree is the
// commonest defect in this repository; one path cannot disagree with
// itself.
//
// An unknown level or format is an error rather than a default. The api
// used to treat anything that was not exactly `json` as console, so
// `log_format: JSON` or a typo silently changed what an aggregator was
// parsing; the hub and the node already refused one, and now all three
// do with the same words.
//
// `log_level_file` is deliberately not read: SPEC 26.1's per-sink level
// is recorded in config.UnreadKeys, and the file sink takes the global
// level. Reading it here for one service and not the others is exactly
// the drift this function exists to prevent.
//
// The setting names are passed to the config accessors literally, which
// is what the declared-and-unread audit in internal/config looks for.
func FromConfig(cfg *config.Config, over Overrides, component string, secrets *redact.Set) (*Logger, error) {
	levelName := over.Level
	if levelName == "" {
		levelName = cfg.String("log_level", "info")
	}
	level, ok := ParseLevel(levelName)
	if !ok {
		return nil, fmt.Errorf("log_level %q is not a level; try error, warn, info, debug, or trace", levelName)
	}
	formatName := over.Format
	if formatName == "" {
		formatName = cfg.String("log_format", "json")
	}
	format, ok := ParseFormat(formatName)
	if !ok {
		return nil, fmt.Errorf("log_format %q is not a format; try json or console", formatName)
	}
	return New(Options{
		Level:   level,
		Format:  format,
		File:    cfg.String("log_file", ""),
		Fields:  map[string]any{"component": component},
		Secrets: secrets,
	})
}

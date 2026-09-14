// Package extpillar connects the external pillar sources of SPEC 12.7
// to the extension model of SPEC 24.
//
// Salt's external pillar is a Python file on the file server that the
// master imports in-process. Here a source is a signed, pinned // lexicon:allow
// extension of kind `pillar`, delivered under `_ext/`, verified on every
// load, and run out of process in a sandbox. The tree still says
// `ext_pillar:` and still names the source the same way, so a migrating
// configuration is recognisable; what changed is underneath it.
package extpillar

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// EntryPoint is the function a pillar extension provides.
//
// Salt's name, because a person porting one is reading Salt's
// documentation and there is nothing to gain by renaming it.
const EntryPoint = "ext_pillar"

// Caller is the part of a loaded extension this needs.
//
// An interface rather than *extension.Loaded so the tests can drive the
// source without a signed bundle on disk.
type Caller interface {
	Call(ctx context.Context, function string, args, kwargs any, callCtx *ext.CallContext) (json.RawMessage, error)
}

// Bridged is one external pillar source running as an extension.
type Bridged struct {
	// SourceName is what `ext_pillar` called it, which is the
	// extension's name.
	SourceName string
	// Ext is the loaded extension.
	Ext Caller
	// Config is the block written under the source's name in
	// `ext_pillar`, handed over untouched. The hub does not interpret
	// it: it belongs to the extension, and a hub that validated it
	// would be a second place to keep the extension's schema.
	Config any
	// Ignore is `fail: ignore` for this source.
	Ignore bool
	// OnSecret receives every string the source returns, for the
	// redactor of SPEC 26.1.
	OnSecret func(string)
}

// Name identifies the source in a diagnostic.
func (b *Bridged) Name() string { return b.SourceName }

// FailSoft reports `ext_pillar_fail: ignore`.
func (b *Bridged) FailSoft() bool { return b.Ignore }

// request is what the extension is handed.
//
// The node's grains and the pillar compiled so far, which is what
// Salt's `ext_pillar(minion_id, pillar, *args)` received, plus the // lexicon:allow
// environment and the source's own configuration block.
type request struct {
	NodeID string          `json:"node_id"`
	Env    string          `json:"env"`
	Grains json.RawMessage `json:"grains"`
	Pillar json.RawMessage `json:"pillar"`
	Config any             `json:"config,omitempty"`
}

// Pillar asks the extension what it contributes.
func (b *Bridged) Pillar(ctx context.Context, req pillar.ExtRequest) (*value.Map, error) {
	if b.Ext == nil {
		return nil, fmt.Errorf("the %q extension is not loaded", b.SourceName)
	}
	grains, err := encodeMap(req.Grains)
	if err != nil {
		return nil, fmt.Errorf("encoding the grains for %q: %w", b.SourceName, err)
	}
	so_far, err := encodeMap(req.Pillar)
	if err != nil {
		return nil, fmt.Errorf("encoding the pillar for %q: %w", b.SourceName, err)
	}
	payload := request{
		NodeID: req.NodeID,
		Env:    req.Env,
		Grains: grains,
		Pillar: so_far,
		Config: b.Config,
	}
	raw, err := b.Ext.Call(ctx, EntryPoint, nil, payload, &ext.CallContext{
		NodeID: req.NodeID,
		Env:    req.Env,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	decoded, err := value.DecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("the %q extension answered with something that is not readable: %w",
			b.SourceName, err)
	}
	out, ok := decoded.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("the %q extension answered with %s; an external pillar source returns a mapping",
			b.SourceName, value.TypeName(decoded))
	}

	// Everything an external pillar source returns is treated as
	// secret. The hub cannot tell which of a source's values are
	// credentials -- only the source knows, and it is out of process --
	// so the safe reading is that a value which arrived on the pillar
	// path is pillar. Over-redacting a log is recoverable; printing a
	// password is not.
	if b.OnSecret != nil {
		offerStrings(out, b.OnSecret)
	}
	return out, nil
}

// encodeMap renders a value map as JSON, or `{}` when it is nil.
//
// An extension must always receive both objects, even when the hub has
// nothing to put in them: an extension written against `grains.os` and
// handed a null would have to guard every read.
func encodeMap(m *value.Map) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage("{}"), nil
	}
	return value.EncodeJSON(m, 0)
}

// offerStrings walks a decoded value and offers every string leaf.
func offerStrings(v any, onSecret func(string)) {
	switch t := v.(type) {
	case *value.Map:
		for _, e := range t.Entries() {
			offerStrings(e.Val, onSecret)
		}
	case []any:
		for _, item := range t {
			offerStrings(item, onSecret)
		}
	case string:
		onSecret(t)
	}
}

// Sources builds one source per entry of a parsed `ext_pillar` list.
//
// An entry naming an extension that is not loaded is an error rather
// than a source that contributes nothing: SPEC 12.7's whole argument is
// that a pillar missing what a source would have supplied is worse than
// no pillar, and that applies just as much when the source never ran.
func Sources(specs []Spec, runtime *extension.Runtime, onSecret func(string)) ([]pillar.ExtSource, error) {
	out := make([]pillar.ExtSource, 0, len(specs))
	for _, spec := range specs {
		loaded, ok := runtime.Get(spec.Name)
		if !ok {
			return nil, fmt.Errorf(
				"`ext_pillar` names the source %q and no extension of that name is installed. "+
					"An external pillar source is an extension of kind `pillar` (SPEC 24): build it, sign it, "+
					"put the bundle under `_ext/` in the tree, and `halite-hub extensions sync`. "+
					"Installed now: %s", spec.Name, installedNames(runtime))
		}
		if kind := loaded.Bundle.Manifest.Kind; kind != "pillar" {
			return nil, fmt.Errorf(
				"`ext_pillar` names %q, which is installed as an extension of kind %q rather than `pillar`",
				spec.Name, kind)
		}
		out = append(out, &Bridged{
			SourceName: spec.Name,
			Ext:        loaded,
			Config:     spec.Config,
			Ignore:     spec.Ignore,
			OnSecret:   onSecret,
		})
	}
	return out, nil
}

func installedNames(runtime *extension.Runtime) string {
	names := runtime.Names()
	if len(names) == 0 {
		return "none"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

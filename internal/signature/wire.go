package signature

import (
	"encoding/json"
	"fmt"
)

// The wire form of a signature: what an extension sends at handshake,
// and what the host reads. SPEC section 15.6.
//
// Defined once, here, and used in both directions. It used to exist
// only as an anonymous struct inside the host's reader, which meant an
// extension author had no way to produce it except by hand — and the
// first extension written against `Signature` itself marshalled the
// parameter types as the integers they are, sent a handshake the host
// refused, and reported no functions at all. A wire format with only a
// reader is a wire format nobody can write.

// Wire is one function signature as it crosses the bridge.
type Wire struct {
	Module     string      `json:"module"`
	Function   string      `json:"function"`
	Doc        string      `json:"doc,omitempty"`
	Mutates    bool        `json:"mutates,omitempty"`
	Platforms  []string    `json:"platforms,omitempty"`
	Privileges []string    `json:"privileges,omitempty"`
	Params     []WireParam `json:"params,omitempty"`
}

// WireParam is one parameter. The type is its name, never its number:
// the number is an implementation detail of this package and would make
// the protocol depend on the order of a Go const block.
type WireParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Doc      string `json:"doc,omitempty"`
	Default  any    `json:"default,omitempty"`
}

// ToWire converts a signature for sending.
func ToWire(sig Signature) Wire {
	out := Wire{
		Module: sig.Module, Function: sig.Function, Doc: sig.Doc,
		Mutates: sig.Mutates, Platforms: sig.Platforms, Privileges: sig.Privileges,
	}
	for _, p := range sig.Params {
		out.Params = append(out.Params, WireParam{
			Name: p.Name, Type: p.Type.String(), Required: p.Required,
			Doc: p.Doc, Default: p.Default,
		})
	}
	return out
}

// Encode renders a signature in the wire form, for an extension's
// handshake.
//
// This is what `bridge.Extension.Functions` wants. Marshalling a
// Signature directly does not produce it.
func Encode(sig Signature) (json.RawMessage, error) {
	raw, err := json.Marshal(ToWire(sig))
	if err != nil {
		return nil, fmt.Errorf("encoding the signature for %s.%s: %w",
			sig.Module, sig.Function, err)
	}
	return raw, nil
}

// EncodeAll renders several, which is the usual case.
func EncodeAll(sigs ...Signature) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(sigs))
	for _, sig := range sigs {
		raw, err := Encode(sig)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// FromWire converts a received signature, resolving the type names.
//
// A type name this build does not know becomes Any rather than an
// error: a newer extension naming a type this host has never heard of
// should lose the checking, not the function.
func FromWire(w Wire) Signature {
	sig := Signature{
		Module: w.Module, Function: w.Function, Doc: w.Doc,
		Mutates: w.Mutates, Platforms: w.Platforms, Privileges: w.Privileges,
	}
	for _, p := range w.Params {
		sig.Params = append(sig.Params, Param{
			Name: p.Name, Type: NamedType(p.Type),
			Required: p.Required, Doc: p.Doc, Default: p.Default,
		})
	}
	return sig
}

// NamedType resolves a wire type name.
func NamedType(name string) Type {
	for t, n := range typeNames {
		if n == name {
			return t
		}
	}
	return Any
}

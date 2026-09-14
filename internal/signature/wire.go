package signature

import (
	"github.com/edlitmus/halite/ext"
)

// Conversion between this package's signatures and the wire shape of
// SPEC 15.6, which lives in `ext` because that is the package an
// extension author writes against.
//
// The wire shape used to exist only as an anonymous struct inside the
// host's reader, so nothing exported could produce it — and the first
// extension written against `Signature` marshalled that instead, sent
// each parameter's type as the integer it is here, and had every
// signature refused. One definition, in the package both sides import,
// is what stops that.

// ToWire converts a signature for sending.
func ToWire(sig Signature) ext.Signature {
	out := ext.Signature{
		Module: sig.Module, Function: sig.Function, Doc: sig.Doc,
		Mutates: sig.Mutates, Platforms: sig.Platforms, Privileges: sig.Privileges,
	}
	for _, p := range sig.Params {
		out.Params = append(out.Params, ext.Param{
			Name: p.Name, Type: p.Type.String(), Required: p.Required,
			Doc: p.Doc, Default: p.Default,
		})
	}
	return out
}

// ToWireAll converts several, which is the usual case.
func ToWireAll(sigs ...Signature) []ext.Signature {
	out := make([]ext.Signature, 0, len(sigs))
	for _, sig := range sigs {
		out = append(out, ToWire(sig))
	}
	return out
}

// FromWire converts a received signature, resolving the type names.
//
// A type name this build does not know becomes Any rather than an
// error: a newer extension naming a type this host has never heard of
// should lose the checking, not the function.
func FromWire(w ext.Signature) Signature {
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

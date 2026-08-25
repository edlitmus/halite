// Package ldap is a bind-only LDAP client.
//
// SPEC 23.3 makes the surface deliberately narrow: BindRequest,
// BindResponse, SearchRequest, SearchResultEntry, SearchResultDone, and
// UnbindRequest, with simple bind over LDAPS or StartTLS. That is
// enough to authenticate an operator and resolve their groups, which is
// all this product needs, and every LDAP feature left out is one that
// cannot go wrong here.
//
// Anonymous bind is refused, plain LDAP without TLS is refused, and
// referrals are not chased.
package ldap

import (
	"encoding/asn1"
	"fmt"
)

// LDAP is BER, and this file is the encoding underneath it.
//
// Written against `encoding/asn1` through `asn1.RawValue`, which gives
// the tag and class control LDAP's application and context tags need
// while leaving the length encoding — the classic place to get this
// wrong — to the standard library.

// The ASN.1 classes LDAP uses.
const (
	classUniversal   = 0
	classApplication = 1
	classContext     = 2
)

// The universal tags LDAP uses.
const (
	tagBoolean     = 1
	tagInteger     = 2
	tagOctetString = 4
	tagNull        = 5
	tagEnumerated  = 10
	tagSequence    = 16
	tagSet         = 17
)

// raw marshals one tag-length-value.
//
// It cannot fail for the shapes this package builds; a failure here
// would be a programming error rather than anything an LDAP server did,
// so it panics rather than making every caller check.
func raw(class, tag int, compound bool, body []byte) []byte {
	out, err := asn1.Marshal(asn1.RawValue{
		Class: class, Tag: tag, IsCompound: compound, Bytes: body,
	})
	if err != nil {
		panic("ldap: encoding " + fmt.Sprint(class, tag) + ": " + err.Error())
	}
	return out
}

// seq is a universal SEQUENCE holding the concatenation of its parts.
func seq(parts ...[]byte) []byte {
	return raw(classUniversal, tagSequence, true, concat(parts...))
}

// set is a universal SET.
func set(parts ...[]byte) []byte {
	return raw(classUniversal, tagSet, true, concat(parts...))
}

// appSeq is `[APPLICATION n] SEQUENCE`, which is how every LDAP
// protocol operation is tagged.
func appSeq(tag int, parts ...[]byte) []byte {
	return raw(classApplication, tag, true, concat(parts...))
}

// ctxSeq is `[n] SEQUENCE` — a context-specific constructed value.
func ctxSeq(tag int, parts ...[]byte) []byte {
	return raw(classContext, tag, true, concat(parts...))
}

// ctxStr is `[n] OCTET STRING` — a context-specific primitive, which is
// how a simple bind carries its password and a filter its assertion.
func ctxStr(tag int, s string) []byte {
	return raw(classContext, tag, false, []byte(s))
}

// str is an OCTET STRING.
func str(s string) []byte {
	return raw(classUniversal, tagOctetString, false, []byte(s))
}

// integer is an INTEGER.
func integer(n int) []byte {
	out, err := asn1.Marshal(n)
	if err != nil {
		panic("ldap: encoding integer: " + err.Error())
	}
	return out
}

// enumerated is an ENUMERATED, which shares INTEGER's content encoding
// and differs only in its tag.
func enumerated(n int) []byte {
	encoded := integer(n)
	// Replace the universal INTEGER tag with ENUMERATED, keeping the
	// length and content the standard library produced.
	return append([]byte{tagEnumerated}, encoded[1:]...)
}

// boolean is a BOOLEAN.
func boolean(b bool) []byte {
	out, err := asn1.Marshal(b)
	if err != nil {
		panic("ldap: encoding boolean: " + err.Error())
	}
	return out
}

// null is a NULL, which is the whole body of an UnbindRequest.
func null() []byte {
	return []byte{tagNull, 0x00}
}

func concat(parts ...[]byte) []byte {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// element is one parsed tag-length-value.
type element struct {
	Class int
	Tag   int
	// Compound reports whether the value holds further elements.
	Compound bool
	// Bytes is the content, with the tag and length removed.
	Bytes []byte
}

// parse reads one element and returns it with whatever followed.
func parse(data []byte) (element, []byte, error) {
	var v asn1.RawValue
	rest, err := asn1.Unmarshal(data, &v)
	if err != nil {
		return element{}, nil, fmt.Errorf("the server sent something that is not BER: %w", err)
	}
	return element{Class: v.Class, Tag: v.Tag, Compound: v.IsCompound, Bytes: v.Bytes}, rest, nil
}

// children reads every element inside a compound one.
func (e element) children() ([]element, error) {
	var out []element
	rest := e.Bytes
	for len(rest) > 0 {
		child, remaining, err := parse(rest)
		if err != nil {
			return nil, err
		}
		out = append(out, child)
		rest = remaining
	}
	return out, nil
}

// text reads a primitive element as a string.
func (e element) text() string { return string(e.Bytes) }

// number reads an INTEGER or ENUMERATED.
//
// Hand-decoded rather than through `asn1.Unmarshal`, because the tag has
// already been consumed and re-synthesising a well-formed INTEGER
// header to hand back would be more code than the seven lines below.
func (e element) number() (int, error) {
	if len(e.Bytes) == 0 {
		return 0, fmt.Errorf("an integer with no content")
	}
	if len(e.Bytes) > 4 {
		// LDAP's integers are message identifiers, result codes, and
		// limits. Nothing legitimate needs more than this, and a
		// server sending one is either broken or probing.
		return 0, fmt.Errorf("an integer of %d bytes, which is longer than anything LDAP uses", len(e.Bytes))
	}
	value := 0
	for _, b := range e.Bytes {
		value = value<<8 | int(b)
	}
	// Two's complement for the negative case, which a result code
	// should never be but a malformed response can be.
	if e.Bytes[0]&0x80 != 0 {
		value -= 1 << (8 * len(e.Bytes))
	}
	return value, nil
}

package ext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The properties SPEC 24.7's compatibility policy rests on.
//
// Every one of them is true today by accident rather than by decision:
// tolerance of an unknown field is `encoding/json`'s default, and this
// project's habits run the other way -- a setting that parses and does
// nothing is a defect here, and somebody reaching for
// `DisallowUnknownFields` would be following the house style. Doing
// that would make every additive change to the protocol a breaking one,
// silently, for every extension already written.
//
// So the policy is only worth writing down if these are held. They are
// held here.

// A field the receiver has never heard of is ignored, not refused.
//
// This is what makes a field addable at all. Without it, an extension
// built against protocol 1 stops working the day a host adds anything.
func TestAnUnknownFieldInAHelloIsIgnored(t *testing.T) {
	// A hello with a field from some later version of the protocol.
	raw := []byte(`{"kind":"hello","protocol":1,"extension_kind":"pillar",` +
		`"a_field_from_the_future":{"nested":[1,2,3]}}`)

	var in bytes.Buffer
	writeRawFrame(t, &in, raw)
	writeRawFrame(t, &in, mustFrame(t, Frame{Kind: FrameShutdown}))

	var out bytes.Buffer
	e := &Extension{
		Name: "demo", Version: "1.0.0", Kind: KindPillar,
		Functions: []Signature{{Module: "demo", Function: "ext_pillar"}},
		Handler:   func(Call) (any, error) { return nil, nil },
	}
	if err := e.serve(&in, &out); err != nil {
		t.Fatalf("an unknown field stopped the handshake: %v", err)
	}
	reply, err := ReadFrame(&out)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != FrameHelloOK {
		t.Errorf("it answered with a %q frame", reply.Kind)
	}
}

// The same in the other direction: a host reading an answer from a
// newer extension ignores what it does not know.
func TestAnUnknownFieldInAnAnswerIsIgnored(t *testing.T) {
	raw := []byte(`{"kind":"result","id":"1","ok":true,"value":{"a":1},` +
		`"a_field_from_the_future":"ignored"}`)
	var buf bytes.Buffer
	writeRawFrame(t, &buf, raw)

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("an unknown field stopped the read: %v", err)
	}
	if !frame.OK || frame.ID != "1" {
		t.Errorf("it read %+v", frame)
	}
	if string(frame.Value) != `{"a":1}` {
		t.Errorf("the value is %s", frame.Value)
	}
}

// An unknown *kind* is refused, which is the other half of the policy:
// a new frame kind is not an additive change, and adding one costs a
// version.
//
// Refusing rather than skipping is deliberate. A receiver that ignored a
// kind it did not know would also ignore a misspelt one, and the sender
// would be waiting for an answer to a frame that was silently dropped.
func TestAnUnknownFrameKindIsRefused(t *testing.T) {
	var in bytes.Buffer
	writeRawFrame(t, &in, mustFrame(t, Frame{Kind: FrameHello, Protocol: ProtocolVersion}))
	writeRawFrame(t, &in, []byte(`{"kind":"a_kind_from_the_future","id":"1"}`))

	var out bytes.Buffer
	e := &Extension{Name: "demo", Version: "1.0.0", Handler: func(Call) (any, error) { return nil, nil }}
	err := e.serve(&in, &out)
	if err == nil {
		t.Fatal("an unknown frame kind was accepted")
	}
	if !strings.Contains(err.Error(), "a_kind_from_the_future") {
		t.Errorf("the error does not name the kind: %v", err)
	}
}

// A frame with no kind at all is refused, so an empty object cannot be
// read as whatever the reader was expecting.
func TestAFrameWithNoKindIsRefused(t *testing.T) {
	var buf bytes.Buffer
	writeRawFrame(t, &buf, []byte(`{"id":"1"}`))
	if _, err := ReadFrame(&buf); err == nil {
		t.Fatal("a frame with no kind was accepted")
	}
}

// The version is one integer and there is no negotiation. A host that
// offered a range would be agreeing to speak versions it no longer
// tests.
func TestTheProtocolVersionIsASingleInteger(t *testing.T) {
	if ProtocolVersion != 1 {
		// Not a guess at what it should be: this is the number SPEC
		// 24.7 and every published extension carry, and changing it is
		// the deliberate act that section describes.
		t.Errorf("the protocol version is %d; changing it is a SPEC 24.7 decision, "+
			"not an implementation detail", ProtocolVersion)
	}
}

// The framing is four bytes of big-endian length. An extension in
// another language reads that from the documentation and gets it wrong
// in exactly one way, so it is pinned here as bytes.
func TestTheLengthPrefixIsFourBytesBigEndian(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{Kind: FrameShutdown}); err != nil {
		t.Fatal(err)
	}
	written := buf.Bytes()
	if len(written) < 4 {
		t.Fatalf("it wrote %d bytes", len(written))
	}
	body := written[4:]
	size := int(written[0])<<24 | int(written[1])<<16 | int(written[2])<<8 | int(written[3])
	if size != len(body) {
		t.Errorf("the prefix says %d and the body is %d bytes", size, len(body))
	}
	// Big-endian: a short frame's high bytes are zero and its low byte
	// is the length. Little-endian would put the length first.
	if written[0] != 0 || written[1] != 0 {
		t.Errorf("the high bytes of a %d byte frame are %v, so this is not big-endian",
			len(body), written[:2])
	}
}

func writeRawFrame(t *testing.T, buf *bytes.Buffer, body []byte) {
	t.Helper()
	buf.Write([]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	})
	buf.Write(body)
}

func mustFrame(t *testing.T, f Frame) []byte {
	t.Helper()
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

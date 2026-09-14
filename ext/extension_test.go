package ext

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// This package is what an extension outside this repository is written
// against, so its surface is a commitment rather than an arrangement
// between two files that change together. These tests are about the
// wire and the handshake, which is the part an author cannot inspect
// from their side.

// drive runs an extension against a scripted host and returns what it
// wrote back.
func drive(t *testing.T, e *Extension, host ...Frame) ([]Frame, error) {
	t.Helper()
	var in bytes.Buffer
	for _, f := range host {
		if err := WriteFrame(&in, f); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := e.serve(&in, &out)

	var frames []Frame
	for {
		f, readErr := ReadFrame(&out)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				break
			}
			t.Fatalf("reading what the extension wrote: %v", readErr)
		}
		frames = append(frames, f)
	}
	return frames, err
}

func hello() Frame {
	return Frame{Kind: FrameHello, Protocol: ProtocolVersion}
}

func anExtension(handler func(Call) (any, error)) *Extension {
	return &Extension{
		Name: "demo", Version: "1.0.0", Kind: KindPillar,
		Functions: []Signature{{
			Module: "demo", Function: "ext_pillar",
			Params: []Param{{Name: "node_id", Type: TypeString, Required: true}},
		}},
		Handler: handler,
	}
}

// A frame survives the round trip, and the length prefix is what
// separates one from the next rather than a newline.
func TestAFrameRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	sent := Frame{
		Kind: FrameResult, ID: "7", OK: true,
		// A newline and a quote inside a value, which is what breaks a
		// newline-delimited protocol.
		Value: json.RawMessage(`{"text":"one\ntwo \"three\""}`),
	}
	if err := WriteFrame(&buf, sent); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, Frame{Kind: FrameShutdown}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != FrameResult || got.ID != "7" || !got.OK {
		t.Errorf("it read %+v", got)
	}
	if string(got.Value) != string(sent.Value) {
		t.Errorf("the value is %s", got.Value)
	}
	// And the second frame is still there, undamaged by the first.
	next, err := ReadFrame(&buf)
	if err != nil || next.Kind != FrameShutdown {
		t.Errorf("the second frame is %+v (%v)", next, err)
	}
}

// A frame larger than the limit is refused before anything is
// allocated for it, so an announced size cannot exhaust memory.
func TestAnOversizedFrameIsRefusedBeforeAllocation(t *testing.T) {
	var buf bytes.Buffer
	size := MaxFrameSize + 1
	buf.Write([]byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)})
	// Deliberately no body: a reader that allocated first would have to
	// wait for bytes that are never coming.
	_, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("an oversized frame was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the error is %q", err)
	}
}

// The handshake reports what the host needs to decide whether to use
// the extension at all.
func TestTheHandshakeCarriesTheDeclaration(t *testing.T) {
	e := anExtension(nil)
	e.Declares = []string{DeclareNetwork}
	frames, err := drive(t, e, hello(), Frame{Kind: FrameShutdown})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Kind != FrameHelloOK {
		t.Fatalf("it answered %+v", frames)
	}
	ok := frames[0]
	if ok.Name != "demo" || ok.Version != "1.0.0" {
		t.Errorf("it named itself %s %s", ok.Name, ok.Version)
	}
	if len(ok.Declares) != 1 || ok.Declares[0] != DeclareNetwork {
		t.Errorf("it declared %v", ok.Declares)
	}
	if len(ok.Functions) != 1 || ok.Functions[0].Function != "ext_pillar" {
		t.Fatalf("it announced %+v", ok.Functions)
	}
	// The type crosses as its name. An integer here is what had every
	// signature refused the first time an extension was written against
	// the host's own signature type.
	if got := ok.Functions[0].Params[0].Type; got != TypeString {
		t.Errorf("the parameter type is %q", got)
	}
}

// The type really is a string on the wire, not merely in the struct.
func TestAParameterTypeIsAStringOnTheWire(t *testing.T) {
	var buf bytes.Buffer
	err := WriteFrame(&buf, Frame{
		Kind:      FrameHelloOK,
		Functions: []Signature{{Module: "m", Function: "f", Params: []Param{{Name: "p", Type: TypeMap}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"type":"map"`) {
		t.Errorf("the wire carries %s", buf.String())
	}
}

// A host speaking another protocol version is refused rather than
// guessed at.
func TestAProtocolMismatchIsRefused(t *testing.T) {
	_, err := drive(t, anExtension(nil), Frame{Kind: FrameHello, Protocol: ProtocolVersion + 1})
	if err == nil {
		t.Fatal("a mismatched protocol was accepted")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Errorf("the error is %q", err)
	}
}

// A host asking for a kind this extension does not provide is refused
// at the handshake, rather than at some call it was never written for.
func TestAKindMismatchIsRefused(t *testing.T) {
	_, err := drive(t, anExtension(nil),
		Frame{Kind: FrameHello, Protocol: ProtocolVersion, Ext: KindReturner})
	if err == nil {
		t.Fatal("a mismatched kind was accepted")
	}
	if !strings.Contains(err.Error(), "returner") {
		t.Errorf("the error is %q", err)
	}
}

// A call is answered with exactly one result frame, after any streaming
// frames it emitted.
func TestStreamingFramesPrecedeTheResult(t *testing.T) {
	e := anExtension(func(call Call) (any, error) {
		call.Log("info", "working")
		call.Progress(1, 2, "halfway")
		return map[string]any{"ok": true}, nil
	})
	frames, err := drive(t, e, hello(),
		Frame{Kind: FrameCall, ID: "1", Function: "ext_pillar"},
		Frame{Kind: FrameShutdown})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.Kind)
	}
	want := []string{FrameHelloOK, FrameLog, FrameProgress, FrameResult}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("it wrote %v", kinds)
	}
	last := frames[len(frames)-1]
	if !last.OK || last.ID != "1" {
		t.Errorf("the result is %+v", last)
	}
}

// A handler that returns an error produces a failed result, and the
// process stays up: one bad call must not cost the pool its process.
func TestAFailingCallIsAResultNotAnExit(t *testing.T) {
	e := anExtension(func(Call) (any, error) {
		return nil, errors.New("the role cannot read that secret")
	})
	frames, err := drive(t, e, hello(),
		Frame{Kind: FrameCall, ID: "1", Function: "ext_pillar"},
		Frame{Kind: FrameCall, ID: "2", Function: "ext_pillar"},
		Frame{Kind: FrameShutdown})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("it wrote %d frames", len(frames))
	}
	for _, f := range frames[1:] {
		if f.OK || !strings.Contains(f.Error, "cannot read that secret") {
			t.Errorf("the result is %+v", f)
		}
	}
}

// A handler that panics names the function rather than taking the
// extension down, where the host would report only that it exited.
func TestAPanickingHandlerNamesTheFunction(t *testing.T) {
	e := anExtension(func(Call) (any, error) { panic("nil map") })
	frames, err := drive(t, e, hello(),
		Frame{Kind: FrameCall, ID: "1", Function: "ext_pillar"},
		Frame{Kind: FrameShutdown})
	if err != nil {
		t.Fatal(err)
	}
	last := frames[len(frames)-1]
	if last.OK {
		t.Fatal("a panic produced a successful result")
	}
	if !strings.Contains(last.Error, "ext_pillar") || !strings.Contains(last.Error, "nil map") {
		t.Errorf("the error is %q", last.Error)
	}
}

// An extension with no handler says so rather than answering nothing.
func TestAnExtensionWithNoHandlerSaysSo(t *testing.T) {
	frames, err := drive(t, anExtension(nil), hello(),
		Frame{Kind: FrameCall, ID: "1", Function: "ext_pillar"},
		Frame{Kind: FrameShutdown})
	if err != nil {
		t.Fatal(err)
	}
	last := frames[len(frames)-1]
	if last.OK || last.Error == "" {
		t.Errorf("the result is %+v", last)
	}
}

// Every kind this package names is one the host will accept.
func TestTheKindsAreConsistent(t *testing.T) {
	for _, k := range Kinds {
		if !ValidKind(k) {
			t.Errorf("%s is listed and not valid", k)
		}
	}
	if ValidKind("pilar") {
		t.Error("a misspelt kind was accepted")
	}
}

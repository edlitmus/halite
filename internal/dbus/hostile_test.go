package dbus

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// signal builds a signal message whose header holds one field, code 1 (the
// path), with the variant signature and value bytes given, and whose body
// is empty.
func signalWithField(vsig string, value []byte) []byte {
	hdr := []byte{1, byte(len(vsig))}
	hdr = append(hdr, vsig...)
	hdr = append(hdr, 0)
	hdr = append(hdr, value...)
	msg := []byte{'l', TypeSignal, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(msg[12:], uint32(len(hdr)))
	msg = append(msg, hdr...)
	for len(msg)%8 != 0 {
		msg = append(msg, 0)
	}
	return msg
}

// What a peer sends cannot panic, loop or allocate without bound in the
// decoder: each of these used to be an index or slice out of range, a loop
// that never ended, a recursion as deep as the message was long, or an
// allocation of whatever length the header claimed. DIVERGENCE 5.544.
func TestTheDecoderRefusesWhatAPeerCanSendThatIsMalformed(t *testing.T) {
	u32 := func(n uint32) []byte { return binary.LittleEndian.AppendUint32(nil, n) }
	deep := func(n int) (string, []byte) {
		// n nested variants, each `v`, then a byte.
		var b []byte
		for i := 0; i < n; i++ {
			b = append(b, 1, 'v', 0)
		}
		return "v", append(b, 1, 'y', 0, 7)
	}
	deepSig, deepVal := deep(5000)

	cases := map[string][]byte{
		"an empty variant signature":     signalWithField("", nil),
		"a bare struct signature":        signalWithField("(", nil),
		"a bare dict signature":          signalWithField("{", nil),
		"an array with no element type":  signalWithField("a", u32(0)),
		"an array of empty structs":      signalWithField("a()", append(u32(8), make([]byte, 8)...)),
		"variants nested thousands deep": signalWithField(deepSig, deepVal),
	}
	for name, msg := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: the decoder panicked: %v", name, r)
				}
			}()
			if _, err := decode(bytes.NewReader(msg)); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}()
	}

	// A length that claims more than the specification allows is refused
	// before anything is allocated for it.
	huge := []byte{'l', TypeSignal, 0, 1, 0, 0, 0, 0xF0, 1, 0, 0, 0, 0, 0, 0, 0}
	if _, err := decode(bytes.NewReader(huge)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a body of 4 GiB: %v", err)
	}
	hugeHeader := []byte{'l', TypeSignal, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0xF9, 0xFF, 0xFF, 0xFF}
	if _, err := decode(bytes.NewReader(hugeHeader)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a header of 4 GiB: %v", err)
	}
}

// A signed 64-bit integer and a double come back as what they are.
func TestSignedAndFloatingValuesDecodeToTheirOwnTypes(t *testing.T) {
	d := &dec{b: binary.LittleEndian.AppendUint64(nil, math.MaxUint64)}
	if v, err := d.getValue("x"); err != nil || v != int64(-1) {
		t.Errorf("x = %#v, %v; want int64(-1)", v, err)
	}
	d = &dec{b: binary.LittleEndian.AppendUint64(nil, math.Float64bits(1.5))}
	if v, err := d.getValue("d"); err != nil || v != 1.5 {
		t.Errorf("d = %#v, %v; want 1.5", v, err)
	}
	d = &dec{b: binary.LittleEndian.AppendUint64(nil, 7)}
	if v, err := d.getValue("t"); err != nil || v != uint64(7) {
		t.Errorf("t = %#v, %v; want uint64(7)", v, err)
	}
}

package dbus

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Message types (D-Bus spec §"Message Types").
const (
	TypeMethodCall   byte = 1
	TypeMethodReturn byte = 2
	TypeError        byte = 3
	TypeSignal       byte = 4
)

// Header field codes.
const (
	fieldPath        byte = 1
	fieldInterface   byte = 2
	fieldMember      byte = 3
	fieldErrorName   byte = 4
	fieldReplySerial byte = 5
	fieldDestination byte = 6
	fieldSender      byte = 7
	fieldSignature   byte = 8
)

// Message is one D-Bus message, header parsed and body unmarshalled per
// its signature. On the way out only Type, Serial, the routing fields
// and Body are read; on the way in every field is populated.
type Message struct {
	Type        byte
	Flags       byte
	Serial      uint32
	ReplySerial uint32
	Path        string
	Interface   string
	Member      string
	ErrorName   string
	Destination string
	Sender      string
	Signature   string
	Body        []any
}

// All integers on the wire here are little-endian: the client always
// writes 'l', and every bus in practice speaks it back.
var order = binary.LittleEndian

// ---- encoding ----

type enc struct{ b []byte }

func (e *enc) pad(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *enc) putByte(v byte)  { e.b = append(e.b, v) }
func (e *enc) putU32(v uint32) { e.pad(4); e.b = order.AppendUint32(e.b, v) }

func (e *enc) putStr(s string) {
	e.putU32(uint32(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

func (e *enc) putSig(s string) {
	e.b = append(e.b, byte(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

// putValue marshals one value of the single complete type `sig`.
func (e *enc) putValue(sig string, v any) error {
	switch sig[0] {
	case 'y':
		b, ok := v.(byte)
		if !ok {
			return typeErr(sig, v)
		}
		e.putByte(b)
	case 'b':
		b, ok := v.(bool)
		if !ok {
			return typeErr(sig, v)
		}
		var n uint32
		if b {
			n = 1
		}
		e.putU32(n)
	case 'u':
		n, ok := v.(uint32)
		if !ok {
			return typeErr(sig, v)
		}
		e.putU32(n)
	case 's', 'o':
		s, ok := v.(string)
		if !ok {
			return typeErr(sig, v)
		}
		e.pad(4)
		e.putStr(s)
	case 'g':
		s, ok := v.(string)
		if !ok {
			return typeErr(sig, v)
		}
		e.putSig(s)
	case 'a':
		return e.putArray(sig[1:], v)
	default:
		return fmt.Errorf("dbus: cannot marshal type %q", sig)
	}
	return nil
}

func (e *enc) putArray(elem string, v any) error {
	e.pad(4)
	lenAt := len(e.b)
	e.b = append(e.b, 0, 0, 0, 0)
	if a := alignOf(elem); a > 4 {
		e.pad(a)
	}
	start := len(e.b)
	switch xs := v.(type) {
	case []string:
		for _, s := range xs {
			if err := e.putValue(elem, s); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range xs {
			if err := e.putValue(elem, x); err != nil {
				return err
			}
		}
	default:
		return typeErr("a"+elem, v)
	}
	order.PutUint32(e.b[lenAt:], uint32(len(e.b)-start))
	return nil
}

func typeErr(sig string, v any) error {
	return fmt.Errorf("dbus: value %#v is not a %q", v, sig)
}

// encode marshals a complete message. Header fields sit at offset 16,
// which is already 8-aligned, so alignment computed against the buffer
// length matches alignment against the message start.
func encode(m *Message) ([]byte, error) {
	e := &enc{b: make([]byte, 0, 128)}
	e.b = append(e.b, 'l', m.Type, m.Flags, 1)
	e.b = append(e.b, 0, 0, 0, 0) // body length, patched below
	e.b = order.AppendUint32(e.b, m.Serial)

	// Header is an array of (byte, variant). Reserve its length word,
	// pad to the struct boundary, then write each present field.
	e.pad(4)
	hdrLenAt := len(e.b)
	e.b = append(e.b, 0, 0, 0, 0)
	e.pad(8)
	hdrStart := len(e.b)

	field := func(code byte, vsig string, val any) error {
		e.pad(8)
		e.putByte(code)
		e.putSig(vsig)
		return e.putValue(vsig, val)
	}
	type f struct {
		when bool
		code byte
		sig  string
		val  any
	}
	for _, x := range []f{
		{m.Path != "", fieldPath, "o", m.Path},
		{m.Interface != "", fieldInterface, "s", m.Interface},
		{m.Member != "", fieldMember, "s", m.Member},
		{m.ErrorName != "", fieldErrorName, "s", m.ErrorName},
		{m.ReplySerial != 0, fieldReplySerial, "u", m.ReplySerial},
		{m.Destination != "", fieldDestination, "s", m.Destination},
		{m.Signature != "", fieldSignature, "g", m.Signature},
	} {
		if !x.when {
			continue
		}
		if err := field(x.code, x.sig, x.val); err != nil {
			return nil, err
		}
	}
	order.PutUint32(e.b[hdrLenAt:], uint32(len(e.b)-hdrStart))

	e.pad(8)
	bodyStart := len(e.b)
	types := splitSig(m.Signature)
	if len(types) != len(m.Body) {
		return nil, fmt.Errorf("dbus: signature %q wants %d args, got %d", m.Signature, len(types), len(m.Body))
	}
	for i, t := range types {
		if err := e.putValue(t, m.Body[i]); err != nil {
			return nil, err
		}
	}
	order.PutUint32(e.b[4:], uint32(len(e.b)-bodyStart))
	return e.b, nil
}

// ---- decoding ----

type dec struct {
	b   []byte
	off int
}

func (d *dec) need(n int) error {
	if d.off+n > len(d.b) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (d *dec) align(n int) {
	for d.off%n != 0 && d.off < len(d.b) {
		d.off++
	}
}

func (d *dec) getByte() (byte, error) {
	if err := d.need(1); err != nil {
		return 0, err
	}
	v := d.b[d.off]
	d.off++
	return v, nil
}

func (d *dec) getU32() (uint32, error) {
	d.align(4)
	if err := d.need(4); err != nil {
		return 0, err
	}
	v := order.Uint32(d.b[d.off:])
	d.off += 4
	return v, nil
}

func (d *dec) getStr() (string, error) {
	n, err := d.getU32()
	if err != nil {
		return "", err
	}
	if err := d.need(int(n) + 1); err != nil {
		return "", err
	}
	s := string(d.b[d.off : d.off+int(n)])
	d.off += int(n) + 1
	return s, nil
}

func (d *dec) getSig() (string, error) {
	if err := d.need(1); err != nil {
		return "", err
	}
	n := int(d.b[d.off])
	d.off++
	if err := d.need(n + 1); err != nil {
		return "", err
	}
	s := string(d.b[d.off : d.off+n])
	d.off += n + 1
	return s, nil
}

// getValue unmarshals one value of the single complete type `sig`.
func (d *dec) getValue(sig string) (any, error) {
	switch sig[0] {
	case 'y':
		return d.getByte()
	case 'b':
		n, err := d.getU32()
		return n != 0, err
	case 'u':
		return d.getU32()
	case 'n', 'q':
		d.align(2)
		if err := d.need(2); err != nil {
			return nil, err
		}
		v := order.Uint16(d.b[d.off:])
		d.off += 2
		return v, nil
	case 'i':
		v, err := d.getU32()
		return int32(v), err
	case 'x', 't', 'd':
		d.align(8)
		if err := d.need(8); err != nil {
			return nil, err
		}
		v := order.Uint64(d.b[d.off:])
		d.off += 8
		return v, nil
	case 's', 'o':
		d.align(4)
		return d.getStr()
	case 'g':
		return d.getSig()
	case 'v':
		vsig, err := d.getSig()
		if err != nil {
			return nil, err
		}
		// A variant unwraps to its inner value: callers asking for a
		// property get the string, not a wrapper.
		return d.getValue(vsig)
	case 'a':
		elem := sig[1:]
		n, err := d.getU32()
		if err != nil {
			return nil, err
		}
		if a := alignOf(elem); a > 4 {
			d.align(a)
		}
		end := d.off + int(n)
		if end > len(d.b) {
			return nil, io.ErrUnexpectedEOF
		}
		var out []any
		for d.off < end {
			v, err := d.getValue(elem)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case '(':
		d.align(8)
		var out []any
		for _, t := range splitSig(sig[1 : len(sig)-1]) {
			v, err := d.getValue(t)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case '{':
		d.align(8)
		inner := splitSig(sig[1 : len(sig)-1])
		if len(inner) != 2 {
			return nil, fmt.Errorf("dbus: bad dict entry %q", sig)
		}
		k, err := d.getValue(inner[0])
		if err != nil {
			return nil, err
		}
		v, err := d.getValue(inner[1])
		if err != nil {
			return nil, err
		}
		return [2]any{k, v}, nil
	default:
		return nil, fmt.Errorf("dbus: cannot unmarshal type %q", sig)
	}
}

// decode reads one whole message from r.
func decode(r io.Reader) (*Message, error) {
	var head [16]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 'l' {
		return nil, fmt.Errorf("dbus: big-endian messages are not supported")
	}
	m := &Message{Type: head[1], Flags: head[2]}
	bodyLen := order.Uint32(head[4:8])
	m.Serial = order.Uint32(head[8:12])
	hdrLen := order.Uint32(head[12:16])

	// Header fields, then padding up to the 8-byte boundary the body
	// begins on.
	hdrPadded := (hdrLen + 7) &^ 7
	buf := make([]byte, hdrPadded)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	hd := &dec{b: buf[:hdrLen]}
	for hd.off < len(hd.b) {
		hd.align(8)
		if hd.off >= len(hd.b) {
			break
		}
		code, err := hd.getByte()
		if err != nil {
			return nil, err
		}
		vsig, err := hd.getSig()
		if err != nil {
			return nil, err
		}
		val, err := hd.getValue(vsig)
		if err != nil {
			return nil, err
		}
		switch code {
		case fieldPath:
			m.Path, _ = val.(string)
		case fieldInterface:
			m.Interface, _ = val.(string)
		case fieldMember:
			m.Member, _ = val.(string)
		case fieldErrorName:
			m.ErrorName, _ = val.(string)
		case fieldReplySerial:
			m.ReplySerial, _ = val.(uint32)
		case fieldDestination:
			m.Destination, _ = val.(string)
		case fieldSender:
			m.Sender, _ = val.(string)
		case fieldSignature:
			m.Signature, _ = val.(string)
		}
	}

	if bodyLen > 0 {
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		bd := &dec{b: body}
		for _, t := range splitSig(m.Signature) {
			v, err := bd.getValue(t)
			if err != nil {
				return nil, err
			}
			m.Body = append(m.Body, v)
		}
	}
	return m, nil
}

// ---- signatures ----

// splitSig breaks a signature into its top-level complete types.
func splitSig(sig string) []string {
	var out []string
	for i := 0; i < len(sig); {
		n := typeLen(sig[i:])
		out = append(out, sig[i:i+n])
		i += n
	}
	return out
}

// typeLen is the length of the one complete type at the start of s.
func typeLen(s string) int {
	switch s[0] {
	case 'a':
		if len(s) == 1 {
			return 1
		}
		return 1 + typeLen(s[1:])
	case '(', '{':
		open := s[0]
		shut := byte(')')
		if open == '{' {
			shut = '}'
		}
		depth := 0
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case open:
				depth++
			case shut:
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(s)
	default:
		return 1
	}
}

// alignOf is the marshalling alignment of the type at the start of s.
func alignOf(s string) int {
	switch s[0] {
	case 'y', 'g', 'v':
		return 1
	case 'n', 'q':
		return 2
	case 'b', 'u', 'i', 's', 'o', 'a':
		return 4
	case 'x', 't', 'd', '(', '{':
		return 8
	default:
		return 1
	}
}

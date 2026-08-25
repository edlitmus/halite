package ldap

// A directory that speaks LDAP, for tests — this package's own and
// those of any package that authenticates through it.
//
// It lives here rather than in a `_test.go` file because `internal/api`
// needs a real directory to test its login path end to end, and a fake
// that only pretends to speak the protocol would leave the one thing
// worth testing — that this client and a directory understand each
// other — untested on both sides. Nothing here imports `testing`, so
// the glue that does stays in the test files.

import (
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"time"
)

// fakeDirectory speaks enough LDAP to answer this client.
//
// A real server rather than a mock of the client's own calls: the point
// of the package is that it produces BER a directory accepts and reads
// BER a directory sends, and only a peer that parses what is written
// can establish that.
type fakeDirectory struct {
	ln  net.Listener
	cfg *tls.Config

	mu sync.Mutex
	// users maps a DN to a password.
	users map[string]string
	// entries are what a search can return, by DN.
	entries map[string]Entry
	// searches records every filter the client sent, decoded back to
	// text, so a test can assert on what was actually asked.
	searches []string
	// startTLSRefused makes the server refuse the upgrade.
	startTLSRefused bool
	// plain serves without TLS, for the refusal test.
	plain bool
}

// newDirectory starts a server on a loopback port.
func newDirectory(cfg *tls.Config) (*fakeDirectory, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d := &fakeDirectory{
		ln:      ln,
		users:   map[string]string{},
		entries: map[string]Entry{},
		cfg:     cfg,
	}
	go d.accept()
	return d, nil
}

// close stops the listener.
func (d *fakeDirectory) close() { _ = d.ln.Close() }

func (d *fakeDirectory) address() string { return d.ln.Addr().String() }

func (d *fakeDirectory) accept() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.serve(conn)
	}
}

func (d *fakeDirectory) serve(conn net.Conn) {
	defer conn.Close()
	d.mu.Lock()
	plain, startTLSRefused := d.plain, d.startTLSRefused
	d.mu.Unlock()

	// LDAPS wraps immediately; StartTLS negotiates first.
	if !plain {
		if wrapped, ok := d.maybeImmediateTLS(conn); ok {
			conn = wrapped
		}
	}

	buf := []byte{}
	chunk := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil && n == 0 {
			return
		}
		for {
			msg, rest, perr := parse(buf)
			if perr != nil {
				break
			}
			buf = rest
			reply, upgrade := d.handle(msg, startTLSRefused)
			if len(reply) > 0 {
				if _, werr := conn.Write(reply); werr != nil {
					return
				}
			}
			if upgrade {
				tlsConn := tls.Server(conn, d.cfg)
				if herr := tlsConn.Handshake(); herr != nil {
					return
				}
				conn = tlsConn
				buf = nil
			}
		}
	}
}

// maybeImmediateTLS peeks: an LDAPS client sends a TLS ClientHello, and
// a StartTLS client sends BER.
func (d *fakeDirectory) maybeImmediateTLS(conn net.Conn) (net.Conn, bool) {
	peeked := &peekConn{Conn: conn}
	first, err := peeked.peek(1)
	if err != nil {
		return conn, false
	}
	// 0x16 is a TLS handshake record; 0x30 is a BER SEQUENCE.
	if first[0] != 0x16 {
		return peeked, false
	}
	tlsConn := tls.Server(peeked, d.cfg)
	if err := tlsConn.Handshake(); err != nil {
		return conn, false
	}
	return tlsConn, true
}

// handle answers one message and reports whether to upgrade after it.
func (d *fakeDirectory) handle(msg element, startTLSRefused bool) ([]byte, bool) {
	children, err := msg.children()
	if err != nil || len(children) < 2 {
		return nil, false
	}
	id, err := children[0].number()
	if err != nil {
		return nil, false
	}
	op := children[1]

	switch op.Tag {
	case opExtendedRequest:
		if startTLSRefused {
			return d.result(id, opExtendedResponse, ResultUnwillingToPerform, "TLS is disabled"), false
		}
		return d.result(id, opExtendedResponse, ResultSuccess, ""), true
	case opBindRequest:
		return d.bind(id, op), false
	case opSearchRequest:
		return d.search(id, op), false
	case opUnbindRequest:
		return nil, false
	}
	return d.result(id, opBindResponse, ResultProtocolError, "unsupported"), false
}

func (d *fakeDirectory) bind(id int, op element) []byte {
	parts, err := op.children()
	if err != nil || len(parts) < 3 {
		return d.result(id, opBindResponse, ResultProtocolError, "malformed bind")
	}
	dn := parts[1].text()
	password := parts[2].text()

	d.mu.Lock()
	want, known := d.users[dn]
	d.mu.Unlock()
	if !known || want != password {
		return d.result(id, opBindResponse, ResultInvalidCredentials, "")
	}
	return d.result(id, opBindResponse, ResultSuccess, "")
}

func (d *fakeDirectory) search(id int, op element) []byte {
	parts, err := op.children()
	if err != nil || len(parts) < 8 {
		return d.result(id, opSearchDone, ResultProtocolError, "malformed search")
	}
	baseDN := parts[0].text()
	filter := describeFilter(parts[6])

	d.mu.Lock()
	d.searches = append(d.searches, filter)
	var matched []Entry
	for _, e := range d.entries {
		if !strings.HasSuffix(e.DN, baseDN) {
			continue
		}
		if entryMatches(e, filter) {
			matched = append(matched, e)
		}
	}
	d.mu.Unlock()

	out := []byte{}
	for _, e := range matched {
		attrs := [][]byte{}
		for name, values := range e.Attributes {
			vals := make([][]byte, 0, len(values))
			for _, v := range values {
				vals = append(vals, str(v))
			}
			attrs = append(attrs, seq(str(name), set(vals...)))
		}
		out = append(out, seq(integer(id),
			appSeq(opSearchEntry, str(e.DN), seq(attrs...)))...)
	}
	return append(out, d.result(id, opSearchDone, ResultSuccess, "")...)
}

func (d *fakeDirectory) result(id, op, code int, message string) []byte {
	return seq(integer(id), appSeq(op, enumerated(code), str(""), str(message)))
}

// describeFilter renders a parsed filter back to RFC 4515 text, which
// is how a test asserts on what the client actually sent — including
// whether a username was escaped.
func describeFilter(f element) string {
	switch f.Tag {
	case filterAnd, filterOr:
		joiner := "&"
		if f.Tag == filterOr {
			joiner = "|"
		}
		children, err := f.children()
		if err != nil {
			return "(?)"
		}
		var b strings.Builder
		b.WriteString("(" + joiner)
		for _, c := range children {
			b.WriteString(describeFilter(c))
		}
		b.WriteString(")")
		return b.String()
	case filterNot:
		children, err := f.children()
		if err != nil || len(children) == 0 {
			return "(?)"
		}
		return "(!" + describeFilter(children[0]) + ")"
	case filterPresent:
		return "(" + f.text() + "=*)"
	case filterEqualityMatch:
		children, err := f.children()
		if err != nil || len(children) < 2 {
			return "(?)"
		}
		// Escaped on the way out, as any RFC 4515 renderer does. A
		// value holding `)` printed literally reads like an injected
		// filter even when the client sent one honest equality match,
		// which is a way to make this test lie in both directions.
		return "(" + children[0].text() + "=" + Escape(children[1].text()) + ")"
	case filterSubstrings:
		children, err := f.children()
		if err != nil || len(children) < 2 {
			return "(?)"
		}
		pieces, err := children[1].children()
		if err != nil {
			return "(?)"
		}
		var b strings.Builder
		b.WriteString("(" + children[0].text() + "=")
		for i, p := range pieces {
			if i > 0 || p.Tag != substringInitial {
				b.WriteString("*")
			}
			b.WriteString(p.text())
		}
		if len(pieces) > 0 && pieces[len(pieces)-1].Tag != substringFinal {
			b.WriteString("*")
		}
		b.WriteString(")")
		return b.String()
	}
	return "(?)"
}

// entryMatches is a small filter evaluator, enough for the shapes these
// tests configure.
func entryMatches(e Entry, filter string) bool {
	filter = strings.TrimSpace(filter)
	if strings.HasPrefix(filter, "(&") {
		for _, part := range splitFilters(filter[2 : len(filter)-1]) {
			if !entryMatches(e, part) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(filter, "(|") {
		for _, part := range splitFilters(filter[2 : len(filter)-1]) {
			if entryMatches(e, part) {
				return true
			}
		}
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(filter, "("), ")")
	attr, value, ok := strings.Cut(inner, "=")
	if !ok {
		return false
	}
	if value == "*" {
		return len(e.Values(attr)) > 0
	}
	if equalFold(attr, "dn") {
		return e.DN == value
	}
	for _, v := range e.Values(attr) {
		if v == value {
			return true
		}
	}
	return false
}

func splitFilters(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			if depth == 0 {
				start = i
			}
			depth++
		case ')':
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
			}
		}
	}
	return out
}

// peekConn lets the fake tell an LDAPS hello from a StartTLS message.
type peekConn struct {
	net.Conn
	buf []byte
}

func (p *peekConn) peek(n int) ([]byte, error) {
	for len(p.buf) < n {
		chunk := make([]byte, 1)
		_ = p.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		read, err := p.Conn.Read(chunk)
		if read > 0 {
			p.buf = append(p.buf, chunk[:read]...)
		}
		if err != nil {
			return nil, err
		}
	}
	return p.buf[:n], nil
}

func (p *peekConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// configure mutates the fake under its lock.
//
// The accept loop is already running by the time a test sets anything
// up, so every write here races the server's own reads. That is the
// harness's problem rather than the client's, and a racy harness is a
// flaky test.
func (d *fakeDirectory) configure(change func(*fakeDirectory)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	change(d)
}

// user adds an account.
func (d *fakeDirectory) user(dn, password string) {
	d.configure(func(f *fakeDirectory) { f.users[dn] = password })
}

// entry adds a directory entry.
func (d *fakeDirectory) entry(e Entry) {
	d.configure(func(f *fakeDirectory) { f.entries[e.DN] = e })
}

// forget removes an entry's attribute.
func (d *fakeDirectory) forget(dn, attribute string) {
	d.configure(func(f *fakeDirectory) {
		e, ok := f.entries[dn]
		if !ok {
			return
		}
		delete(e.Attributes, attribute)
		f.entries[dn] = e
	})
}

// asked returns the filters the client has sent.
func (d *fakeDirectory) asked() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.searches...)
}

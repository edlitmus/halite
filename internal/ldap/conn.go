package ldap

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The protocol operations SPEC 23.3 names, and no others.
const (
	opBindRequest      = 0
	opBindResponse     = 1
	opUnbindRequest    = 2
	opSearchRequest    = 3
	opSearchEntry      = 4
	opSearchDone       = 5
	opSearchReference  = 19
	opExtendedRequest  = 23
	opExtendedResponse = 24
)

// startTLSOID is the extended operation that upgrades a plain
// connection, RFC 4511 section 4.14.
const startTLSOID = "1.3.6.1.4.1.1466.20.2036"

// Search scopes.
const (
	ScopeBaseObject   = 0
	ScopeSingleLevel  = 1
	ScopeWholeSubtree = 2
)

// MaxMessageSize bounds one response.
//
// A search that matches a large group returns a large message, and a
// server that is hostile or broken can claim any length it likes. Eight
// megabytes is far past any group this is asked about.
const MaxMessageSize = 8 << 20

// Conn is one connection to a directory.
type Conn struct {
	conn net.Conn
	// nextID assigns message identifiers, which a response is matched
	// against.
	mu     sync.Mutex
	nextID int
	// bound records that a successful non-anonymous bind has happened,
	// so a search cannot be issued on a connection that has not
	// authenticated.
	bound bool
	// buf holds bytes read past the end of one message.
	buf []byte
}

// DialOptions is how to reach a directory.
type DialOptions struct {
	// Address is host:port.
	Address string
	// TLS chooses how the connection is protected. SPEC 23.3 refuses
	// plain LDAP, so this is either `ldaps` or `starttls`.
	TLS string
	// CAFile verifies the directory's certificate against an estate's
	// own CA, which is the common case for a directory.
	CAFile string
	// ServerName overrides the name verified in the certificate, for a
	// directory reached by an address that is not its name.
	ServerName string
	// Timeout bounds the dial and every read.
	Timeout time.Duration
}

// TLS modes.
const (
	TLSLDAPS    = "ldaps"
	TLSStartTLS = "starttls"
)

// Dial opens a connection.
//
// There is no plaintext mode. SPEC 23.3 says so, and the reason is that
// a simple bind puts an operator's password on the wire: a directory
// client that can be configured without TLS will be, on the day
// somebody is debugging something.
func Dial(opts DialOptions) (*Conn, error) {
	if opts.Address == "" {
		return nil, errors.New("an LDAP connection needs an address")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cfg, err := tlsConfig(opts)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}

	switch opts.TLS {
	case TLSLDAPS:
		conn, err := tls.DialWithDialer(dialer, "tcp", opts.Address, cfg)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", opts.Address, err)
		}
		return &Conn{conn: conn}, nil
	case TLSStartTLS:
		plain, err := dialer.Dial("tcp", opts.Address)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", opts.Address, err)
		}
		c := &Conn{conn: plain}
		if err := c.startTLS(cfg, timeout); err != nil {
			plain.Close()
			return nil, err
		}
		return c, nil
	case "":
		return nil, errors.New("an LDAP connection needs `ldaps` or `starttls`; there is no plaintext mode")
	}
	return nil, fmt.Errorf("%q is not an LDAP TLS mode; use `ldaps` or `starttls`", opts.TLS)
}

func tlsConfig(opts DialOptions) (*tls.Config, error) {
	name := opts.ServerName
	if name == "" {
		host, _, err := net.SplitHostPort(opts.Address)
		if err != nil {
			return nil, fmt.Errorf("%q is not host:port", opts.Address)
		}
		name = host
	}
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	if opts.CAFile != "" {
		pem, err := os.ReadFile(filepath.Clean(opts.CAFile))
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificate", opts.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// startTLS upgrades a plain connection.
//
// The response is checked before the handshake begins: a server that
// refuses the extended operation and a client that starts a handshake
// anyway is a client that will fall back to plaintext when somebody
// disables the directory's TLS.
func (c *Conn) startTLS(cfg *tls.Config, timeout time.Duration) error {
	id := c.nextMessageID()
	request := seq(
		integer(id),
		appSeq(opExtendedRequest, ctxStr(0, startTLSOID)),
	)
	if err := c.write(request, timeout); err != nil {
		return err
	}
	response, err := c.read(timeout)
	if err != nil {
		return err
	}
	op, err := operationOf(response, id)
	if err != nil {
		return err
	}
	if op.Tag != opExtendedResponse {
		return fmt.Errorf("the server answered StartTLS with operation %d", op.Tag)
	}
	result, err := resultOf(op)
	if err != nil {
		return err
	}
	if result.Code != ResultSuccess {
		return fmt.Errorf("the server refused StartTLS: %s", result)
	}

	tlsConn := tls.Client(c.conn, cfg)
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("the StartTLS handshake failed: %w", err)
	}
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	c.conn = tlsConn
	return nil
}

// Bind authenticates with a simple bind.
//
// An empty DN or an empty password is refused before anything is sent.
// RFC 4513 says an empty password makes a bind anonymous, and a
// directory answers success to it — so a client that passes an empty
// password through authenticates anybody who submits a login form with
// the password field blank.
func (c *Conn) Bind(dn, password string, timeout time.Duration) error {
	if dn == "" {
		return errors.New("a bind needs a distinguished name; anonymous bind is refused")
	}
	if password == "" {
		return errors.New("a bind needs a password; an empty one is an anonymous bind, which is refused")
	}
	id := c.nextMessageID()
	request := seq(
		integer(id),
		appSeq(opBindRequest,
			integer(3),
			str(dn),
			ctxStr(0, password),
		),
	)
	if err := c.write(request, timeout); err != nil {
		return err
	}
	response, err := c.read(timeout)
	if err != nil {
		return err
	}
	op, err := operationOf(response, id)
	if err != nil {
		return err
	}
	if op.Tag != opBindResponse {
		return fmt.Errorf("the server answered a bind with operation %d", op.Tag)
	}
	result, err := resultOf(op)
	if err != nil {
		return err
	}
	if result.Code != ResultSuccess {
		return result
	}
	c.mu.Lock()
	c.bound = true
	c.mu.Unlock()
	return nil
}

// Entry is one SearchResultEntry.
type Entry struct {
	DN string
	// Attributes maps a name to its values. A name absent from the map
	// was not returned; one present with no values was returned empty.
	Attributes map[string][]string
}

// Values returns one attribute's values, case-insensitively: a
// directory answers `memberOf` however the request spelled it, and
// Active Directory does not agree with OpenLDAP about the case.
func (e Entry) Values(name string) []string {
	if v, ok := e.Attributes[name]; ok {
		return v
	}
	for k, v := range e.Attributes {
		if equalFold(k, name) {
			return v
		}
	}
	return nil
}

// SearchRequest is what to look for.
type SearchRequest struct {
	BaseDN string
	Scope  int
	// Filter is BER, from ParseFilter.
	Filter []byte
	// Attributes to return. Empty asks for all of them, which is worth
	// avoiding: a directory entry can be large and none of it is
	// needed beyond the few attributes configured.
	Attributes []string
	// SizeLimit bounds how many entries the server returns.
	SizeLimit int
	// TimeLimit bounds how long it spends, in seconds.
	TimeLimit int
}

// Search runs one search and reads every entry.
//
// Referrals are not chased: SPEC 23.3 says so, and following one means
// authenticating against a server the estate did not configure.
// A SearchResultReference is counted and ignored.
func (c *Conn) Search(req SearchRequest, timeout time.Duration) ([]Entry, error) {
	c.mu.Lock()
	bound := c.bound
	c.mu.Unlock()
	if !bound {
		return nil, errors.New("this connection has not bound; a search before a successful bind is refused")
	}

	attributes := make([][]byte, 0, len(req.Attributes))
	for _, a := range req.Attributes {
		attributes = append(attributes, str(a))
	}
	id := c.nextMessageID()
	request := seq(
		integer(id),
		appSeq(opSearchRequest,
			str(req.BaseDN),
			enumerated(req.Scope),
			enumerated(0), // neverDerefAliases
			integer(req.SizeLimit),
			integer(req.TimeLimit),
			boolean(false), // typesOnly
			req.Filter,
			seq(attributes...),
		),
	)
	if err := c.write(request, timeout); err != nil {
		return nil, err
	}

	var entries []Entry
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return nil, errors.New("the search did not finish in time")
		}
		response, err := c.read(timeout)
		if err != nil {
			return nil, err
		}
		op, err := operationOf(response, id)
		if err != nil {
			return nil, err
		}
		switch op.Tag {
		case opSearchEntry:
			entry, err := entryOf(op)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
			if req.SizeLimit > 0 && len(entries) > req.SizeLimit {
				// The server was asked for a bound and ignored it.
				return nil, fmt.Errorf("the server returned more than the %d entries it was asked for",
					req.SizeLimit)
			}
		case opSearchReference:
			// A referral. Not chased.
			continue
		case opSearchDone:
			result, err := resultOf(op)
			if err != nil {
				return nil, err
			}
			if result.Code != ResultSuccess && result.Code != ResultSizeLimitExceeded {
				return nil, result
			}
			return entries, nil
		default:
			return nil, fmt.Errorf("the server answered a search with operation %d", op.Tag)
		}
	}
}

// Close sends an UnbindRequest and closes the connection.
//
// The unbind is a courtesy the protocol asks for and gets no response;
// a directory that logs connections logs a clean close rather than a
// reset.
func (c *Conn) Close() error {
	id := c.nextMessageID()
	request := seq(
		integer(id),
		raw(classApplication, opUnbindRequest, false, nil),
	)
	_ = c.write(request, 2*time.Second)
	return c.conn.Close()
}

func (c *Conn) nextMessageID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Conn) write(b []byte, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err := c.conn.Write(b)
	return err
}

// read returns one whole LDAPMessage.
//
// BER is self-delimiting, so this reads until the buffered bytes hold a
// complete element and keeps whatever came after for the next call.
func (c *Conn) read(timeout time.Duration) (element, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if len(c.buf) > 0 {
			if msg, rest, err := parse(c.buf); err == nil {
				c.buf = rest
				return msg, nil
			}
			// Not a whole message yet, which is not an error until the
			// connection ends.
		}
		if len(c.buf) > MaxMessageSize {
			return element{}, fmt.Errorf("the server sent more than %d bytes without a complete message",
				MaxMessageSize)
		}
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return element{}, err
		}
		chunk := make([]byte, 8192)
		n, err := c.conn.Read(chunk)
		if n > 0 {
			c.buf = append(c.buf, chunk[:n]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(c.buf) == 0 {
				return element{}, errors.New("the directory closed the connection")
			}
			return element{}, err
		}
	}
}

// operationOf unwraps an LDAPMessage and checks its identifier.
//
// A response carrying another message's identifier is either a server
// that has confused two requests or a response that was not asked for;
// either way, reading it as the answer to this request is wrong.
func operationOf(message element, wantID int) (element, error) {
	children, err := message.children()
	if err != nil {
		return element{}, err
	}
	if len(children) < 2 {
		return element{}, fmt.Errorf("an LDAP message with %d parts", len(children))
	}
	id, err := children[0].number()
	if err != nil {
		return element{}, err
	}
	if id != wantID {
		return element{}, fmt.Errorf("the server answered message %d with a response for message %d",
			wantID, id)
	}
	return children[1], nil
}

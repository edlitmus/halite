package returner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func init() {
	register("syslog", true, func(opts Options) (Returner, error) {
		return newSyslogReturner(opts)
	})
}

// syslogReturner writes RFC 5424 messages.
//
// Hand-rolled rather than through `log/syslog`, for two reasons that
// both matter here: the standard package speaks RFC 3164, the older
// format with a two-digit day and no structured data, and it does not
// exist on Windows, which SPEC 27.1 puts in tier 1. The format is
// twenty lines.
type syslogReturner struct {
	opts     Options
	facility int
	hostname string

	mu   sync.Mutex
	conn net.Conn
}

// facilities are the RFC 5424 names an operator would write.
var facilities = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "syslog": 5,
	"lpr": 6, "news": 7, "uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19,
	"local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

func newSyslogReturner(opts Options) (*syslogReturner, error) {
	name := opts.SyslogFacility
	if name == "" {
		name = "daemon"
	}
	facility, ok := facilities[name]
	if !ok {
		return nil, fmt.Errorf("%q is not a syslog facility; try daemon, local0 through local7, or one of %s",
			name, strings.Join(facilityNames(), ", "))
	}
	if opts.SyslogTag == "" {
		opts.SyslogTag = "halite"
	}
	if opts.SyslogTLS && opts.SyslogAddress == "" {
		return nil, fmt.Errorf("syslog over TLS needs an address")
	}
	host := opts.NodeID
	if host == "" {
		host, _ = os.Hostname()
	}
	if host == "" {
		host = "-"
	}
	return &syslogReturner{opts: opts, facility: facility, hostname: host}, nil
}

func facilityNames() []string {
	out := make([]string, 0, len(facilities))
	for name := range facilities {
		out = append(out, name)
	}
	return out
}

func (r *syslogReturner) Name() string { return "syslog" }

func (r *syslogReturner) Return(ctx context.Context, ret *job.Return) error {
	raw, err := json.Marshal(ret)
	if err != nil {
		return err
	}
	// A failed job is a warning and a successful one is informational,
	// so an estate can filter on severity without parsing the payload.
	severity := 6
	if !ret.Success {
		severity = 4
	}
	return r.send(severity, string(ret.JID), raw)
}

func (r *syslogReturner) Event(ctx context.Context, e *eventbus.Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.send(6, e.Tag, raw)
}

// send writes one RFC 5424 message.
//
// `<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID - MSG`, with the
// structured-data field empty and the message the JSON. A receiver that
// wants fields parses the JSON; one that does not gets a line it can
// grep, which is what syslog is for.
func (r *syslogReturner) send(severity int, msgID string, payload []byte) error {
	priority := r.facility*8 + severity
	stamp := r.opts.now().UTC().Format("2006-01-02T15:04:05.000000Z")
	header := "<" + strconv.Itoa(priority) + ">1 " + stamp + " " +
		printableASCII(r.hostname) + " " + printableASCII(r.opts.SyslogTag) + " " +
		strconv.Itoa(os.Getpid()) + " " + printableASCII(msgID) + " - "

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.connect(); err != nil {
		return err
	}
	// One write, and a newline, because a stream receiver frames on it.
	// Two writes would let a concurrent sender interleave a message
	// into the middle of this one.
	message := append([]byte(header), payload...)
	message = append(message, '\n')
	if _, err := r.conn.Write(message); err != nil {
		// A dead connection is dropped so the next attempt redials,
		// rather than writing into a socket that will never work again.
		r.conn.Close()
		r.conn = nil
		return err
	}
	return nil
}

// printableASCII replaces what RFC 5424 does not allow in a header
// field. A field is space-delimited, so a value with a space in it
// would move every field after it.
func printableASCII(v string) string {
	if v == "" {
		return "-"
	}
	var b strings.Builder
	for _, c := range v {
		if c <= 32 || c > 126 {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (r *syslogReturner) connect() error {
	if r.conn != nil {
		return nil
	}
	if r.opts.SyslogAddress == "" {
		conn, err := localSyslog()
		if err != nil {
			return err
		}
		r.conn = conn
		return nil
	}
	network := r.opts.SyslogNetwork
	if network == "" {
		network = "tcp"
	}
	timeout := r.opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	if !r.opts.SyslogTLS {
		conn, err := dialer.Dial(network, r.opts.SyslogAddress)
		if err != nil {
			return err
		}
		r.conn = conn
		return nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if r.opts.SyslogCAFile != "" {
		pool, err := certPool(r.opts.SyslogCAFile)
		if err != nil {
			return err
		}
		cfg.RootCAs = pool
	}
	conn, err := tls.DialWithDialer(dialer, network, r.opts.SyslogAddress, cfg)
	if err != nil {
		return err
	}
	r.conn = conn
	return nil
}

// localSyslog finds the local socket.
//
// The path differs by system and there is no portable way to ask, so
// the known ones are tried in turn — which is what every syslog client
// does, including the standard library's.
func localSyslog() (net.Conn, error) {
	paths := []string{"/dev/log", "/var/run/syslog", "/var/run/log"}
	var lastErr error
	for _, network := range []string{"unixgram", "unix"} {
		for _, path := range paths {
			conn, err := net.Dial(network, path)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
	}
	return nil, fmt.Errorf("no local syslog socket: %w", lastErr)
}

func (r *syslogReturner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	return err
}

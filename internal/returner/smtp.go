package returner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func init() {
	register("smtp", true, func(opts Options) (Returner, error) {
		if opts.SMTPAddress == "" {
			return nil, fmt.Errorf("the smtp returner needs an address, as host:port")
		}
		if opts.SMTPFrom == "" {
			return nil, fmt.Errorf("the smtp returner needs a from address")
		}
		if len(opts.SMTPTo) == 0 {
			return nil, fmt.Errorf("the smtp returner needs at least one recipient")
		}
		return &smtpReturner{opts: opts}, nil
	})
}

// smtpReturner mails a return.
//
// For the case SPEC 20.3 has in mind: a small number of returns that a
// person should see, not a stream. An estate that mails every return
// from every node has built a denial of service against its own mail
// server, and this returner does not pretend otherwise — it makes one
// connection per message and says so here rather than batching behind
// the operator's back.
type smtpReturner struct{ opts Options }

func (r *smtpReturner) Name() string { return "smtp" }

func (r *smtpReturner) Return(ctx context.Context, ret *job.Return) error {
	raw, err := json.MarshalIndent(ret, "", "  ")
	if err != nil {
		return err
	}
	outcome := "succeeded"
	if !ret.Success {
		outcome = "FAILED"
	}
	subject := r.subject(fmt.Sprintf("%s %s on %s", ret.Fun, outcome, ret.NodeID))
	return r.send(ctx, subject, raw)
}

func (r *smtpReturner) Event(ctx context.Context, e *eventbus.Event) error {
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return r.send(ctx, r.subject(e.Tag), raw)
}

func (r *smtpReturner) subject(detail string) string {
	if r.opts.SMTPSubject != "" {
		return r.opts.SMTPSubject
	}
	return "halite: " + detail
}

func (r *smtpReturner) send(ctx context.Context, subject string, body []byte) error {
	message := r.message(subject, body)

	timeout := r.opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", r.opts.SMTPAddress)
	if err != nil {
		return err
	}
	defer conn.Close()

	host, _, err := net.SplitHostPort(r.opts.SMTPAddress)
	if err != nil {
		host = r.opts.SMTPAddress
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()

	if r.opts.SMTPTLS {
		// STARTTLS, and a refusal if the server will not: a credential
		// sent in the clear because the server said no is exactly the
		// downgrade the setting was turned on to prevent.
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s does not offer STARTTLS and tls is required", r.opts.SMTPAddress)
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if r.opts.SMTPUsername != "" {
		if !r.opts.SMTPTLS {
			return fmt.Errorf("smtp credentials are refused without tls; they would go in the clear")
		}
		auth := smtp.PlainAuth("", r.opts.SMTPUsername, r.opts.SMTPPassword, host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(r.opts.SMTPFrom); err != nil {
		return err
	}
	for _, to := range r.opts.SMTPTo {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// message builds the RFC 5322 message.
func (r *smtpReturner) message(subject string, body []byte) []byte {
	var b strings.Builder
	b.WriteString("From: " + headerSafe(r.opts.SMTPFrom) + "\r\n")
	b.WriteString("To: " + headerSafe(strings.Join(r.opts.SMTPTo, ", ")) + "\r\n")
	b.WriteString("Subject: " + headerSafe(subject) + "\r\n")
	b.WriteString("Date: " + r.opts.now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Content-Type: application/json; charset=utf-8\r\n")
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.Write(body)
	return []byte(b.String())
}

// headerSafe strips what would end a header line early.
//
// A job's function name reaches the subject, and a return's node id
// reaches it too. Neither is chosen by this program, and a newline in
// either would let the sender write headers of its own — which is
// header injection, and how a return becomes a mail to somebody else.
func headerSafe(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

func (r *smtpReturner) Close() error { return nil }

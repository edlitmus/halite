package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/returner"
)

// openReturner builds the configured returner of SPEC 20.3.
//
// At startup rather than at first use, and fatally: a returner that is
// misconfigured is discovered here, by the operator who wrote it, and
// not three weeks later by the absence of the returns they were relying
// on. The whole reason `returner:` exists is that somebody wants the
// returns somewhere.
func (n *node) openReturner(post func(context.Context, *job.Return) error) {
	name := n.cfg.String("returner", "local")
	stateDir := n.cfg.String("state_dir", config.DefaultStateDir)

	opts := returner.Options{
		StateDir: stateDir,
		NodeID:   n.nodeID,
		Post:     post,
		Timeout:  n.cfg.Duration("returner_timeout", 30*time.Second),
		Log: func(level, msg string, kv ...any) {
			if n.log == nil {
				return
			}
			switch level {
			case "warn":
				n.log.Warn(msg, kv...)
			default:
				n.log.Info(msg, kv...)
			}
		},

		Path:      n.cfg.String("returner_file", ""),
		MaxBytes:  n.cfg.Int("returner_file_max_size", 0),
		KeepFiles: int(n.cfg.Int("returner_file_keep", 5)),

		SyslogAddress:  n.cfg.String("returner_syslog_address", ""),
		SyslogNetwork:  n.cfg.String("returner_syslog_network", "tcp"),
		SyslogTag:      n.cfg.String("returner_syslog_tag", "halite"),
		SyslogFacility: n.cfg.String("returner_syslog_facility", "daemon"),
		SyslogTLS:      n.cfg.Bool("returner_syslog_tls", false),
		SyslogCAFile:   n.cfg.String("returner_syslog_ca_file", ""),

		URL:         n.cfg.String("returner_webhook_url", ""),
		CAFile:      n.cfg.String("returner_webhook_ca_file", ""),
		Secret:      returnerSecret(n),
		MaxAttempts: int(n.cfg.Int("returner_webhook_attempts", 5)),
		SpoolDir:    filepath.Join(stateDir, "returner-spool"),
		SpoolMax:    n.cfg.Int("returner_spool_max_size", 256<<20),

		SMTPAddress:  n.cfg.String("returner_smtp_address", ""),
		SMTPFrom:     n.cfg.String("returner_smtp_from", ""),
		SMTPTo:       splitList(n.cfg.String("returner_smtp_to", "")),
		SMTPSubject:  n.cfg.String("returner_smtp_subject", ""),
		SMTPUsername: n.cfg.String("returner_smtp_username", ""),
		SMTPPassword: n.cfg.String("returner_smtp_password", ""),
		SMTPTLS:      n.cfg.Bool("returner_smtp_tls", true),
	}

	built, err := returner.New(name, opts)
	if err != nil {
		cli.Fatalf("returner: %v", err)
	}
	n.returns = built
}

// returnerSecret reads the webhook signing secret from a file when one
// is named, and from the configuration otherwise.
//
// The file is the one to use. A secret in the node configuration is a
// secret in whatever the configuration is distributed by, which for
// most estates is the state tree it is also managing.
func returnerSecret(n *node) string {
	if path := n.cfg.String("returner_webhook_secret_file", ""); path != "" {
		raw, err := config.ReadSecretFile(path)
		if err != nil {
			cli.Fatalf("returner: %v", err)
		}
		return raw
	}
	return n.cfg.String("returner_webhook_secret", "")
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

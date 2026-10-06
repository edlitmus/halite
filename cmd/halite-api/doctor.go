package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/redact"
)

// runDoctor checks this service and says what to fix.
//
// The API had no `doctor`, and so nothing checked the three certificates
// it depends on: the one it presents to callers, the operator certificate
// it presents to the hub, and the CA it verifies the hub with. The hub's
// own `doctor` does not cover them -- they are named in `api.yaml`, which
// is another service's configuration -- so the gap was not one the hub
// could close. DIVERGENCE 5.160.
//
// Nothing here changes anything and nothing starts a listener: it reads
// the same configuration and the same directories `serve` would, which is
// what makes it safe to run against a service that is already running.
func runDoctor(args *cli.Args) int {
	root := args.Flag("root", config.DefaultRoot)
	path := args.Flag("config", "")
	cfg, loadErr := config.Load(config.API, config.LoadOptions{
		Path: path, Root: root, AllowMissing: true,
	})
	if cfg == nil {
		// Without a configuration there is nothing to check against, and
		// reporting three skips would hide the one fact that matters.
		fmt.Fprintf(os.Stderr, "halite-api doctor: %v\n", loadErr)
		return 1
	}
	shown := path
	if shown == "" {
		shown = root
	}

	report := doctor.Run(context.Background(), doctor.RoleAPI, []doctor.Check{
		doctor.ConfigValidity(shown, loadErr, cfg.Warnings, true),
		apiCertificateCheck(args, cfg),
		apiConnectivityCheck(args, cfg),
		apiFIPSCheck(),
	})

	// `nested` is this command's default, as it is for `token` and
	// `account`: halite-api has never had the hub's "summary" sentinel,
	// so there is no word here that ParseFormat does not know.
	format, err := cli.ParseFormat(args.Flag("out", "nested"))
	if err != nil {
		cli.Usagef("%v", err)
	}
	secrets := doctorSecrets(cfg)
	cli.Redact = secrets.Scrub
	if err := writeDoctor(os.Stdout, report, format, shown, secrets); err != nil {
		cli.Fatalf("%v", err)
	}
	return report.ExitCode()
}

// doctorSecrets is the redactor this report is scrubbed with: the
// configured values whose names say they are secret, as `serve` seeds
// its own.
//
// The hub's and the node's doctors scrub what they print, both ways
// they print it, because a check prints what it found. This one did not.
// No check here reads a secret, and none of a set of malformed
// configurations carrying one got it into the report -- so this is not a
// leak that was seen, but the one output of the three programs that SPEC
// 26.1's "scrubbed at the sink" did not cover. DIVERGENCE 5.221.
func doctorSecrets(cfg *config.Config) *redact.Set {
	secrets := redact.New()
	for _, v := range cfg.SecretValues() {
		secrets.AddTree(v)
	}
	return secrets
}

// writeDoctor prints the report, scrubbed, in the format asked for.
func writeDoctor(w io.Writer, report doctor.Report, format cli.Format, shown string, secrets *redact.Set) error {
	if format == cli.JSON || format == cli.YAML {
		return cli.Write(w, secrets.ScrubValue(doctor.Value(report)), format, 0)
	}
	_, err := fmt.Fprint(w, secrets.Scrub(fmt.Sprintf("halite-api doctor — %s\n\n", shown)+report.Text()))
	return err
}

// apiCertificateCheck covers all three certificates this service holds.
//
// Three, and they do not share a clock. The serving certificate is
// whatever the operator put in `tls_cert` -- often a long-lived
// self-signed one, since callers are configured with it by hand -- while
// the operator certificate it presents to the hub is issued by
// `keys operator create` with a 720h default and reissued in one command.
// Checking them against a single window would mean either nagging about
// the first or missing the second. Same reasoning as the hub's, 5.159.
func apiCertificateCheck(args *cli.Args, cfg *config.Config) doctor.Check {
	const servingNotice = 30 * 24 * time.Hour
	const operatorNotice = 7 * 24 * time.Hour

	files := pki.Files{Dir: args.Flag("pki-dir", cfg.PathUnderRoot("pki_dir", "pki"))}
	certs := map[string]doctor.Expected{}

	// The serving certificate is named by path rather than by the pki
	// layout, because an operator may point `tls_cert` anywhere -- at a
	// certificate from their own CA, or at one this hub issued.
	if p := args.Flag("tls-cert", cfg.String("tls_cert", "")); p != "" {
		label := "the certificate this service presents"
		cert, err := readCertFile(p)
		switch {
		case err != nil:
			certs[label+" (unreadable: "+err.Error()+")"] = doctor.Expected{
				Cert: &x509.Certificate{}, WarnWithin: servingNotice,
			}
		default:
			certs[label] = doctor.Expected{Cert: cert, WarnWithin: servingNotice}
		}
	}

	// The operator certificate this service authenticates to the hub
	// with, under whatever name `api_operator` or `--as` gives it. An
	// expired one is the failure this whole exercise started from: the
	// hub refuses the handshake and every request through the API stops,
	// while the certificate sits on disk looking present.
	name := args.Flag("as", cfg.String("api_operator", "api"))
	operatorFile := pki.OperatorCertFile(name)
	operatorLabel := "the operator certificate for " + name
	if p := args.Flag("cert", ""); p != "" {
		cert, err := readCertFile(p)
		if err == nil {
			certs[operatorLabel] = doctor.Expected{Cert: cert, WarnWithin: operatorNotice}
		} else {
			certs[operatorLabel+" (unreadable: "+err.Error()+")"] = doctor.Expected{
				Cert: &x509.Certificate{}, WarnWithin: operatorNotice,
			}
		}
	} else if files.Exists(operatorFile) {
		cert, err := files.ReadCert(operatorFile)
		if err != nil {
			certs[operatorLabel+" (unreadable: "+err.Error()+")"] = doctor.Expected{
				Cert: &x509.Certificate{}, WarnWithin: operatorNotice,
			}
		} else {
			certs[operatorLabel] = doctor.Expected{Cert: cert, WarnWithin: operatorNotice}
		}
	} else {
		// Named by the configuration and not on disk. This service
		// cannot reach the hub without it, so it fails rather than
		// being skipped: `serve` would exit on the same file.
		certs[operatorLabel] = doctor.Expected{
			Absence:    doctor.Fail,
			WarnWithin: operatorNotice,
			AbsentDetail: "there is no operator certificate for " + name + " at " +
				files.Path(operatorFile) + ", so this service cannot authenticate to the hub",
		}
	}

	// The CA this service verifies the hub with.
	if files.Exists(pki.CACertFile) {
		if cert, err := files.ReadCert(pki.CACertFile); err == nil {
			certs["the hub's CA"] = doctor.Expected{Cert: cert, WarnWithin: servingNotice}
		} else {
			certs["the hub's CA (unreadable: "+err.Error()+")"] = doctor.Expected{
				Cert: &x509.Certificate{}, WarnWithin: servingNotice,
			}
		}
	}
	return doctor.CertificateExpiry(certs, time.Now())
}

// readCertFile reads a certificate named by a configured path rather than
// by the pki layout.
func readCertFile(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pki.DecodeCert(raw)
}

// apiConnectivityCheck asks the hub whether it is there.
//
// The API is a hub client and nothing it serves works without one, so
// "the hub answers" is the second question an operator has after "does my
// configuration load".
func apiConnectivityCheck(args *cli.Args, cfg *config.Config) doctor.Check {
	address := args.Flag("hub", cfg.String("hub", ""))
	return doctor.Connectivity(address, func(ctx context.Context) (string, time.Duration, error) {
		s := &service{cfg: cfg, root: args.Flag("root", config.DefaultRoot)}
		client, err := hubClient(s, args)
		if err != nil {
			// The certificate check reports the same material in its own
			// row, so this says why the probe could not be made rather
			// than repeating the remedy.
			return "", 0, err
		}
		started := time.Now()
		answer, err := client.Health(ctx)
		return answer, time.Since(started), err
	})
}

// apiFIPSCheck is the hub's check with this process's own build in it.
func apiFIPSCheck() doctor.Check {
	state := doctor.FIPSState{
		Artifact: fips.Artifact(),
		Enabled:  fips.Enabled(),
		Module:   fips.Module(),
		Platform: runtime.GOOS,
	}
	on, ok, why := fips.KernelMode()
	if ok {
		state.Kernel = &on
	} else {
		state.NoKernel = why
	}
	return doctor.FIPSConsistency(state)
}

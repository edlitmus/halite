package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/value"
)

// runDoctor is SPEC 26.4's diagnostics for a node.
//
// It gathers the inputs and internal/doctor decides what they mean. The
// split is deliberate: every judgement in these checks — how close to
// expiry is too close, whether an unsigned extension is a warning or a
// failure, what a FIPS mismatch means on a platform with no kernel FIPS
// mode — is worth testing without a node to run it on, and none of it
// could be tested from here.
//
// Nothing here changes anything. An operator runs this when something
// is already wrong, and a diagnostic with a side effect is one nobody
// dares run twice.
func runDoctor(args *cli.Args) int {
	n := setup(args)
	ctx := context.Background()

	report := doctor.Run(ctx, doctor.RoleNode, []doctor.Check{
		nodeConfigCheck(args, n),
		nodeCertificateCheck(args, n),
		nodeConnectivityCheck(args, n),
		nodeClockCheck(args, n),
		nodeFileServerCheck(n),
		nodePillarCheck(n),
		nodeDiskCheck(n),
		nodeExtensionCheck(n),
		doctor.ModuleVerification(builtin.New().Trust()),
		nodeFIPSCheck(n),
	})

	switch n.format {
	case cli.JSON, cli.YAML:
		n.out(doctorValue(report))
	default:
		fmt.Printf("halite-node doctor — %s\n\n", n.nodeID)
		fmt.Print(report.Text())
	}
	return report.ExitCode()
}

// doctorValue renders the report for `--out json` or `yaml`, so that a
// state can read it. `doctor` in a state's `onlyif` is one of the
// reasons SPEC 26.4 argues for making it one command.
func doctorValue(r doctor.Report) *value.Map {
	checks := value.NewMap(len(r.Results))
	for _, res := range r.Results {
		checks.Set(res.Name, value.MapOf(
			"status", string(res.Status),
			"detail", res.Detail,
			"remedy", res.Remedy,
		))
	}
	counts := value.NewMap(4)
	for status, n := range r.Counts() {
		counts.Set(string(status), n)
	}
	return value.MapOf(
		"role", r.Role,
		"worst", string(r.Worst()),
		"counts", counts,
		"checks", checks,
	)
}

// nodeConfigCheck re-reads the configuration from disk.
//
// Re-read rather than reported from what this process is running,
// because the interesting case is a file edited since the service
// started: this process is fine and the next restart is not, which is a
// failure found at the worst possible moment otherwise.
func nodeConfigCheck(args *cli.Args, n *node) doctor.Check {
	path := args.Flag("config", "")
	root := args.Flag("root", config.DefaultRoot)
	fresh, err := config.Load(config.Node, config.LoadOptions{
		Path: path, Root: root, AllowMissing: true,
	})
	shown := path
	if shown == "" {
		shown = root
	}
	var warnings []string
	if fresh != nil {
		warnings = fresh.Warnings
	}
	// Compared on the warnings the two loads produced rather than by
	// deep equality: a Config holds compiled state that differs between
	// two loads of the same bytes, and a check reporting "edited" every
	// run is one nobody reads.
	unchanged := err == nil && len(warnings) == len(n.cfg.Warnings)
	return doctor.ConfigValidity(shown, err, warnings, unchanged)
}

func nodeCertificateCheck(args *cli.Args, n *node) doctor.Check {
	files := pki.Files{Dir: args.Flag("pki-dir", n.cfg.String("pki_dir", config.DefaultPKIDir))}
	certs := map[string]*x509.Certificate{}
	for label, name := range map[string]string{
		"this node's certificate": pki.NodeCertFile,
		"the hub's CA":            pki.CACertFile,
	} {
		if !files.Exists(name) {
			continue
		}
		cert, err := files.ReadCert(name)
		if err != nil {
			// Reported as already expired rather than skipped: nothing
			// will connect with it either way, and a check that stays
			// quiet about a file it could not parse is one that passes
			// on a broken node.
			certs[label+" (unreadable: "+err.Error()+")"] = &x509.Certificate{}
			continue
		}
		certs[label] = cert
	}
	// Fourteen days: long enough that a renewal is this fortnight's
	// work rather than tonight's, short enough not to be background
	// noise for a quarter.
	return doctor.CertificateExpiry(certs, time.Now(), 14*24*time.Hour)
}

func nodeConnectivityCheck(args *cli.Args, n *node) doctor.Check {
	hub := n.cfg.String("hub", "")
	return doctor.Connectivity(hub, func(ctx context.Context) (string, time.Duration, error) {
		client, _ := n.hubClient(args)
		started := time.Now()
		answer, err := client.Health(ctx)
		return answer, time.Since(started), err
	})
}

func nodeClockCheck(args *cli.Args, n *node) doctor.Check {
	// A minute: the hub's Date header is accurate to a second, and
	// nothing this decides — a job's window, a certificate's validity —
	// turns on less than minutes.
	const tolerate = time.Minute
	if n.cfg.String("hub", "") == "" {
		return doctor.ClockSkew(0, fmt.Errorf("no hub is configured"), tolerate)
	}
	client, _ := n.hubClient(args)
	// The health endpoint because it needs no certificate: a node whose
	// certificate has expired still wants to know whether its clock is
	// why.
	at, err := client.HealthDate(context.Background())
	if err != nil {
		return doctor.ClockSkew(0, err, tolerate)
	}
	return doctor.ClockSkew(time.Since(at), nil, tolerate)
}

func nodeFileServerCheck(n *node) doctor.Check {
	roots := n.cfg.StringSlice("file_roots:" + n.env)
	var unreadable error
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			unreadable = err
			break
		}
	}
	source := "local roots for " + n.env
	if n.cfg.String("hub", "") != "" && len(roots) == 0 {
		// A node managed from a hub compiles against the hub's tree,
		// which the connectivity check already covers. Saying that is
		// more use than reporting no roots as a fault.
		return doctor.FileServerReachable("the hub's file server", []string{"the hub"}, nil)
	}
	return doctor.FileServerReachable(source, roots, unreadable)
}

func nodePillarCheck(n *node) doctor.Check {
	p, err := n.compilePillarOrErr()
	keys := 0
	if p != nil {
		keys = p.Len()
	}
	return doctor.PillarCompiles(err, keys)
}

func nodeDiskCheck(n *node) doctor.Check {
	return doctor.DiskFree(freeSpaceOf(
		n.cfg.String("state_dir", config.DefaultStateDir),
		n.cfg.String("cache_dir", config.DefaultCacheDir),
	), 1<<30, 128<<20)
}

// freeSpaceOf measures every directory that exists, once each.
//
// A gigabyte warns and 128 MiB fails, in bytes rather than a
// percentage: 2% is comfortable on a large disk and nothing on a small
// one, and what stops working is a write of a particular size.
func freeSpaceOf(dirs ...string) map[string]doctor.FreeSpace {
	usage := map[string]doctor.FreeSpace{}
	for _, dir := range doctor.DirsOf(dirs...) {
		free, err := doctor.Free(dir)
		usage[dir] = doctor.FreeSpace{Free: free, Err: err}
	}
	return usage
}

func nodeExtensionCheck(n *node) doctor.Check {
	var trust []doctor.ExtensionTrust
	if n.extensions != nil {
		for _, name := range n.extensions.Names() {
			loaded, ok := n.extensions.Get(name)
			if !ok || loaded.Bundle == nil {
				continue
			}
			trust = append(trust, doctor.ExtensionTrust{
				Name:   name,
				Signed: loaded.Bundle.SignedBy != "",
			})
		}
	}
	return doctor.ExtensionSignatures(
		n.cfg.Bool("require_signed_extensions", false), trust)
}

// nodeFIPSCheck gathers the two halves SPEC 27.4 asks to be compared.
//
// The kernel's state comes from this node's own `fips_mode` grain, so
// the check cannot disagree with what a tree targeting on that grain
// sees. It is passed as a pointer because on the BSDs and macOS the
// grain is a hardcoded false — correct there, since a kernel FIPS mode
// is a Linux concept — and "off" is not the same answer as "there is no
// such switch". The check gives different advice for the two, which
// matters: a warning that fires on every FreeBSD host is one nobody
// reads.
func nodeFIPSCheck(n *node) doctor.Check {
	state := doctor.FIPSState{
		Artifact: fips.Artifact(),
		Enabled:  fips.Enabled(),
		Module:   fips.Module(),
		Platform: runtime.GOOS,
	}
	if kernelHasFIPSMode() {
		kernel, _ := value.Traverse(n.grains, "fips_mode", ":")
		on, _ := kernel.(bool)
		state.Kernel = &on
	} else {
		state.NoKernel = runtime.GOOS + " has no kernel FIPS mode"
	}
	return doctor.FIPSConsistency(state)
}

// kernelHasFIPSMode reports whether this platform has a kernel FIPS
// mode at all, which is where the grain's false means "off" rather than
// "no such thing".
//
// Linux has `/proc/sys/crypto/fips_enabled` and Windows has the policy
// value the grain reads. The BSDs and macOS have neither, and
// internal/grains sets the grain false there so a template does not have
// to guard for the platform — a good choice there and the wrong one
// here.
func kernelHasFIPSMode() bool {
	switch runtime.GOOS {
	case "linux", "windows":
		return true
	default:
		return false
	}
}

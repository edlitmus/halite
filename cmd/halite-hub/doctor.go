package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/fips"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/value"
)

// runDoctor is SPEC 26.4's diagnostics for a hub.
//
// Fewer checks than a node's, and the difference is the point: a hub
// has no hub to be disconnected from or out of step with, and running
// "connectivity: skipped, this is a hub" on every invocation would be
// noise on every run rather than information on any. `internal/doctor`
// decides which checks belong to which role.
//
// Nothing here changes anything, and nothing here starts a listener: it
// reads the same configuration and the same directories `serve` would,
// which is what makes it safe to run on a hub that is already running.
func runDoctor(args *cli.Args) int {
	root := args.Flag("root", config.DefaultRoot)
	path := args.Flag("config", "")
	cfg, loadErr := config.Load(config.Hub, config.LoadOptions{
		Path: path, Root: root, AllowMissing: true,
	})
	if cfg == nil {
		// Without a configuration there is nothing to check against,
		// and reporting nine skips would hide the one fact that
		// matters.
		fmt.Fprintf(os.Stderr, "halite-hub doctor: %v\n", loadErr)
		return 1
	}
	shown := path
	if shown == "" {
		shown = root
	}

	report := doctor.Run(context.Background(), doctor.RoleHub, []doctor.Check{
		doctor.ConfigValidity(shown, loadErr, cfg.Warnings, true),
		hubCertificateCheck(args, cfg),
		hubFileServerCheck(cfg),
		hubPillarCheck(cfg),
		hubDiskCheck(cfg),
		hubQueueCheck(cfg),
		hubFIPSCheck(),
	})

	fmt.Printf("halite-hub doctor — %s\n\n", shown)
	fmt.Print(report.Text())
	return report.ExitCode()
}

// doctorValue renders the report for `--out json` or `yaml`.
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

// hubCertificateCheck reads the hub's own certificate and its CA.
//
// The CA is the one that matters most and is the one nobody watches: it
// is issued for years, so it is never the thing anybody is thinking
// about, and when it lapses every node in the estate stops at once.
func hubCertificateCheck(args *cli.Args, cfg *config.Config) doctor.Check {
	files := pki.Files{Dir: args.Flag("pki-dir", cfg.String("pki_dir", config.DefaultPKIDir))}
	certs := map[string]*x509.Certificate{}
	for label, name := range map[string]string{
		"this hub's certificate": pki.HubCertFile,
		"the enrollment CA":      pki.CACertFile,
	} {
		if !files.Exists(name) {
			continue
		}
		cert, err := files.ReadCert(name)
		if err != nil {
			certs[label+" (unreadable: "+err.Error()+")"] = &x509.Certificate{}
			continue
		}
		certs[label] = cert
	}
	// Thirty days rather than the node's fourteen. Reissuing a CA means
	// every node re-enrolling or every certificate being reissued
	// against a new chain, which is a change to schedule rather than an
	// afternoon's work.
	return doctor.CertificateExpiry(certs, time.Now(), 30*24*time.Hour)
}

func hubFileServerCheck(cfg *config.Config) doctor.Check {
	env := cfg.String("env", "base")
	roots := cfg.StringSlice("file_roots:" + env)
	var unreadable error
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			unreadable = err
			break
		}
	}
	return doctor.FileServerReachable("file_roots for "+env, roots, unreadable)
}

// hubPillarCheck compiles the pillar the way a node asking for one
// would, but for no node in particular.
//
// A pillar that does not compile is the failure that takes a whole
// estate out at once, and it takes it out at the moment a node asks
// rather than at the moment somebody edits the tree. Compiling it here
// moves the discovery to where an operator is already looking.
func hubPillarCheck(cfg *config.Config) doctor.Check {
	env := cfg.String("pillarenv", cfg.String("env", "base"))
	roots := cfg.StringSlice("pillar_roots:" + env)
	if len(roots) == 0 {
		return doctor.PillarCompiles(nil, 0)
	}
	// Compiled for a node with no grains, which reaches the top file and
	// every SLS matching `'*'` — the majority of a pillar tree, and the
	// part that breaks. An SLS behind a grain target is not reached and
	// cannot be: which files a real node gets is a question about that
	// node, and `halite-node doctor` compiles the whole of its own. The
	// two together are the answer; this half is the one an operator can
	// run without leaving the hub.
	strategy, _ := value.ParseStrategy(cfg.String("pillar_source_merging_strategy", "smart"))
	c := &pillar.Compiler{
		Loader: fileserver.NewRoots(map[string][]string{env: roots}),
		Config: pillar.Config{
			Env:      env,
			NodeID:   "halite-hub-doctor",
			Grains:   value.NewMap(0),
			Strategy: strategy,
		},
	}
	compiled := c.Compile()
	if compiled == nil {
		return doctor.PillarCompiles(fmt.Errorf("the compiler returned nothing"), 0)
	}
	if err := compiled.Err(); err != nil {
		return doctor.PillarCompiles(err, 0)
	}
	keys := 0
	if compiled.Pillar != nil {
		keys = compiled.Pillar.Len()
	}
	return doctor.PillarCompiles(nil, keys)
}

func hubDiskCheck(cfg *config.Config) doctor.Check {
	usage := map[string]doctor.FreeSpace{}
	for _, dir := range doctor.DirsOf(
		cfg.String("state_dir", config.DefaultStateDir),
		cfg.String("cache_dir", config.DefaultCacheDir),
	) {
		free, err := doctor.Free(dir)
		usage[dir] = doctor.FreeSpace{Free: free, Err: err}
	}
	// A hub that cannot write a job record refuses the job, so the
	// floor is higher than a node's: it is the difference between a
	// slow estate and one that cannot be told to do anything.
	return doctor.DiskFree(usage, 2<<30, 256<<20)
}

// hubQueueCheck reports the reactor's bound rather than its depth.
//
// The depth is a running hub's, and this command deliberately does not
// start one or connect to one — a diagnostic that needed the thing it
// diagnoses to be healthy would be no use on the day it is not. What it
// can say from the configuration is what the bound will be, which is
// the number that turns a backlog into loss, and `halite-hub metrics`
// reads the live depth from a hub that is running.
func hubQueueCheck(cfg *config.Config) doctor.Check {
	if len(cfg.StringSlice("reactor")) == 0 && cfg.String("reactor", "") == "" {
		return doctor.QueueDepths(nil)
	}
	// The reactor's own default, which it reads when nothing overrides
	// it. Named here rather than repeated as a literal so the two
	// cannot drift; DIVERGENCE has a section on documentation that
	// copies a number instead of reading it.
	depth := hub.DefaultReactorQueueDepth
	return doctor.QueueDepths(map[string]doctor.QueueDepth{
		"reactor (configured bound; run `halite-hub metrics` for the live depth)": {
			Depth: 0, Limit: depth,
		},
	})
}

// hubFIPSCheck compares this binary's mode with the host kernel's.
//
// The same argument as the node's, and it applies to a hub for the
// stronger reason: the hub is where enrollment happens, so a hub whose
// cryptography is not what an assessment covers is every node's problem
// rather than one node's.
func hubFIPSCheck() doctor.Check {
	state := doctor.FIPSState{
		Artifact: fips.Artifact(),
		Enabled:  fips.Enabled(),
		Module:   fips.Module(),
		Platform: runtime.GOOS,
	}
	on, ok, why := kernelFIPSMode()
	if ok {
		state.Kernel = &on
	} else {
		state.NoKernel = why
	}
	return doctor.FIPSConsistency(state)
}

// kernelFIPSMode reports the host kernel's FIPS state, and whether the
// platform has one at all.
//
// The second return is the part that matters. On the BSDs and macOS
// there is no kernel FIPS mode, and "off" is not the same answer as
// "there is no such switch": the check gives different advice for the
// two, and a warning that fires on every FreeBSD host is one nobody
// reads.
func kernelFIPSMode() (on bool, known bool, why string) {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/sys/crypto/fips_enabled")
		if err != nil {
			// Absent on a kernel built without the FIPS option, which
			// is a kernel that cannot be in FIPS mode. That is an
			// answer, not a missing one.
			return false, true, ""
		}
		return strings.TrimSpace(string(b)) == "1", true, ""
	case "windows":
		// The `fips_mode` grain reads the policy value, and a hub
		// reading it again here would be a second implementation of
		// the same lookup that the two would eventually disagree
		// about. Reported as not known from here rather than as
		// absent, which is what Windows is not.
		return false, false, "this command does not read the Windows FIPS policy value; " +
			"`halite-node doctor` reports it, through the fips_mode grain"
	default:
		return false, false, runtime.GOOS + " has no kernel FIPS mode"
	}
}

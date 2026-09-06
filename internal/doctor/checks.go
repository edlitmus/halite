package doctor

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The checks SPEC 26.4 names, other than the FIPS one, which has enough
// to say for itself to live in fips.go.
//
// Every one of them takes its inputs rather than reaching for them. A
// diagnostic that can only be exercised on a broken node is one nobody
// can be sure of, and the whole point of these is to be trusted at the
// moment somebody is already having a bad day.

// ConfigValidity reports whether the configuration this process is
// running with loaded cleanly.
//
// `loadErr` is what re-reading the configuration from disk gave. It is
// re-read rather than reported from memory on purpose: the interesting
// case is a file edited since the service started, where the running
// process is fine and the next restart is not. That is a failure an
// operator finds at the worst possible moment otherwise.
func ConfigValidity(path string, loadErr error, unknownKeys []string, restartsClean bool) Check {
	return Check{
		Name:  "configuration validity",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "configuration validity"}
			if loadErr != nil {
				res.Status = Fail
				res.Detail = fmt.Sprintf("%s does not load: %v", path, loadErr)
				res.Remedy = "This process is running on what it read at startup; the next " +
					"restart will not start.\nFix the file, then run this again before restarting."
				return res
			}
			if len(unknownKeys) > 0 {
				sort.Strings(unknownKeys)
				res.Status = Warn
				res.Detail = fmt.Sprintf("%s loads, with %d setting(s) this build does not read: %s",
					path, len(unknownKeys), strings.Join(unknownKeys, ", "))
				res.Remedy = "A setting nothing reads is a setting that is not doing what it " +
					"looks like it is doing.\nCheck the spelling against `halite-node config` " +
					"or remove it."
				return res
			}
			if !restartsClean {
				res.Status = Warn
				res.Detail = fmt.Sprintf("%s loads but differs from what this process started with", path)
				res.Remedy = "The file has been edited since this process started. Restart to " +
					"pick it up, or revert the file."
				return res
			}
			res.Status = Pass
			res.Detail = path + " loads, and matches what this process is running"
			return res
		},
	}
}

// CertificateExpiry checks every certificate this process depends on.
//
// Warns rather than fails on an approaching expiry, and the window is
// generous, because the point is to be told in time to act rather than
// on the morning it stops working. An expired one is a failure: nothing
// will connect.
func CertificateExpiry(certs map[string]*x509.Certificate, now time.Time, warnWithin time.Duration) Check {
	return Check{
		Name:  "certificate validity and expiry",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "certificate validity and expiry"}
			if len(certs) == 0 {
				res.Status = Skip
				res.Detail = "this process holds no certificate"
				res.Remedy = "A node enrols with `halite-node enroll`; a hub's own is created " +
					"when its CA is."
				return res
			}
			names := make([]string, 0, len(certs))
			for name := range certs {
				names = append(names, name)
			}
			sort.Strings(names)

			var expired, expiring, details []string
			for _, name := range names {
				cert := certs[name]
				if cert == nil {
					continue
				}
				left := cert.NotAfter.Sub(now)
				switch {
				case now.Before(cert.NotBefore):
					expired = append(expired, fmt.Sprintf("%s is not valid until %s",
						name, cert.NotBefore.UTC().Format(time.RFC3339)))
				case left <= 0:
					expired = append(expired, fmt.Sprintf("%s expired %s ago",
						name, roughly(-left)))
				case left <= warnWithin:
					expiring = append(expiring, fmt.Sprintf("%s expires in %s", name, roughly(left)))
				default:
					details = append(details, fmt.Sprintf("%s: %s left", name, roughly(left)))
				}
			}
			switch {
			case len(expired) > 0:
				res.Status = Fail
				res.Detail = strings.Join(append(expired, expiring...), "; ")
				res.Remedy = "Nothing will connect with an expired certificate.\n" +
					"A node renews with `halite-node renew`; a hub's is reissued from its CA."
			case len(expiring) > 0:
				res.Status = Warn
				res.Detail = strings.Join(expiring, "; ")
				res.Remedy = "Renew before it lapses: `halite-node renew` on a node. A " +
					"certificate that expires while the node is connected keeps that " +
					"connection and is refused on the next one, so the symptom appears at a\n" +
					"restart rather than at the expiry."
			default:
				res.Status = Pass
				res.Detail = strings.Join(details, "; ")
			}
			return res
		},
	}
}

// Connectivity checks that the hub answers.
//
// `probe` returns what the hub said and how long it took. A node that
// cannot reach its hub is the single most common ticket there is, and
// the useful part of the answer is which of the three ways it failed:
// no route, no TLS, or no hub.
func Connectivity(hub string, probe func(context.Context) (string, time.Duration, error)) Check {
	return Check{
		Name:  "connectivity",
		Roles: []string{RoleNode},
		Run: func(ctx context.Context) Result {
			res := Result{Name: "connectivity"}
			if hub == "" {
				res.Status = Skip
				res.Detail = "no hub is configured; this node runs from local roots"
				res.Remedy = "Set `hub` in the configuration to manage this node from one."
				return res
			}
			answer, took, err := probe(ctx)
			if err != nil {
				res.Status = Fail
				res.Detail = fmt.Sprintf("%s did not answer: %v", hub, err)
				res.Remedy = "In order: is the name resolvable and the port reachable; does " +
					"the hub trust this node's CA and this node the hub's; is the hub running.\n" +
					"`halite-node doctor` checks the certificate separately — if that " +
					"passed, this is the network or the hub."
				return res
			}
			res.Status = Pass
			res.Detail = fmt.Sprintf("%s answered in %s: %s", hub, roughly(took), strings.TrimSpace(answer))
			return res
		},
	}
}

// ClockSkew compares this host's clock with the hub's.
//
// The hub decides every expiry — a job's window, a certificate's
// validity — from its own clock, so a node whose clock disagrees is a
// node whose jobs expire at times it does not expect. The measurement
// is the Date header on the hub's health response, which is accurate to
// a second: skew that matters here is minutes, and asking for better
// would mean a protocol change for no gain.
func ClockSkew(skew time.Duration, err error, tolerate time.Duration) Check {
	return Check{
		Name:  "clock skew against the hub",
		Roles: []string{RoleNode},
		Run: func(context.Context) Result {
			res := Result{Name: "clock skew against the hub"}
			if err != nil {
				res.Status = Skip
				res.Detail = "the hub's clock could not be read: " + err.Error()
				res.Remedy = "This needs the hub to answer; see the connectivity check."
				return res
			}
			ahead := "ahead of"
			d := skew
			if d < 0 {
				ahead, d = "behind", -d
			}
			if d > tolerate {
				res.Status = Warn
				res.Detail = fmt.Sprintf("this host is %s %s the hub", roughly(d), ahead)
				res.Remedy = "The hub decides when a job's window closes and when a " +
					"certificate is valid, from its own clock. A node that disagrees sees " +
					"jobs expire early or late.\nRun NTP on both."
				return res
			}
			res.Status = Pass
			res.Detail = fmt.Sprintf("within %s of the hub (%s %s)", roughly(tolerate), roughly(d), ahead)
			return res
		},
	}
}

// FileServerReachable checks that the tree a run compiles against can
// be read.
func FileServerReachable(source string, roots []string, err error) Check {
	return Check{
		Name:  "file server reachability",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "file server reachability"}
			if err != nil {
				res.Status = Fail
				res.Detail = fmt.Sprintf("%s is not readable: %v", source, err)
				res.Remedy = "A state naming `salt://` or `halite://` cannot be applied until " +
					"this works.\nFor local roots, check the paths exist and this account can " +
					"read them; for a hub, see the connectivity check."
				return res
			}
			if len(roots) == 0 {
				res.Status = Warn
				res.Detail = source + " is reachable and has no roots"
				res.Remedy = "Nothing can be served, so every `source:` will fail to resolve. " +
					"Set `file_roots`."
				return res
			}
			res.Status = Pass
			res.Detail = fmt.Sprintf("%s: %d root(s)", source, len(roots))
			return res
		},
	}
}

// PillarCompiles reports whether this node's pillar compiles.
//
// A pillar that does not compile is the failure that takes a whole
// estate out at once, because every state that reads it fails and the
// message names the template rather than the tree.
func PillarCompiles(err error, keys int) Check {
	return Check{
		Name:  "pillar compilation",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "pillar compilation"}
			if err != nil {
				res.Status = Fail
				res.Detail = "the pillar does not compile: " + firstLine(err.Error())
				res.Remedy = "Every state that reads pillar fails until this does. " +
					"`halite-node pillar items` shows the same error with the file in it."
				return res
			}
			res.Status = Pass
			res.Detail = fmt.Sprintf("compiles, %d top-level key(s)", keys)
			return res
		},
	}
}

// DiskFree checks the directories this process writes to.
//
// Free bytes rather than a percentage: a 2% margin is comfortable on a
// large disk and nothing at all on a small one, and the thing that
// stops working is a write of a particular size.
func DiskFree(usage map[string]FreeSpace, warnBelow, failBelow uint64) Check {
	return Check{
		Name:  "disk space",
		Roles: []string{RoleNode, RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "disk space"}
			if len(usage) == 0 {
				res.Status = Skip
				res.Detail = "none of this role's directories exist yet"
				res.Remedy = "They are created by `make install` or at first use. Nothing " +
					"writes to a directory that is not there, so there is nothing to measure."
				return res
			}
			names := make([]string, 0, len(usage))
			for name := range usage {
				names = append(names, name)
			}
			sort.Strings(names)

			var low, empty, fine []string
			unreportable := 0
			for _, name := range names {
				u := usage[name]
				if errors.Is(u.Err, ErrNotReportable) {
					unreportable++
					continue
				}
				if u.Err != nil {
					empty = append(empty, fmt.Sprintf("%s: %v", name, u.Err))
					continue
				}
				switch {
				case u.Free < failBelow:
					empty = append(empty, fmt.Sprintf("%s: %s free", name, bytesOf(u.Free)))
				case u.Free < warnBelow:
					low = append(low, fmt.Sprintf("%s: %s free", name, bytesOf(u.Free)))
				default:
					fine = append(fine, fmt.Sprintf("%s: %s free", name, bytesOf(u.Free)))
				}
			}
			if unreportable == len(usage) {
				res.Status = Skip
				res.Detail = fmt.Sprintf("free space cannot be read on this platform (%d directories)",
					unreportable)
				res.Remedy = "Go's `syscall` exposes neither statfs nor statvfs here. Check " +
					"the state and cache directories with the platform's own tool."
				return res
			}
			switch {
			case len(empty) > 0:
				res.Status = Fail
				res.Detail = strings.Join(append(empty, low...), "; ")
				res.Remedy = "A hub that cannot write a job record refuses the job; a node " +
					"that cannot write its spool loses returns.\nThe job cache and the event " +
					"bus both prune on a schedule — check `retention` and `max_bytes` before " +
					"adding a disk."
			case len(low) > 0:
				res.Status = Warn
				res.Detail = strings.Join(low, "; ")
				res.Remedy = "Check `retention` and `max_bytes` for the job cache and the " +
					"event bus; both prune by age and size and both default generously."
			default:
				res.Status = Pass
				res.Detail = strings.Join(fine, "; ")
			}
			return res
		},
	}
}

// FreeSpace is one directory's answer.
type FreeSpace struct {
	Free uint64
	Err  error
}

// ErrNotReportable is what Free returns where the platform has no way
// to ask.
//
// A sentinel rather than a message, because "this platform cannot tell
// you" and "this directory could not be read" are different answers and
// the check gives different ones back: the first is a skip with the
// reason, the second is a directory an operator should look at. The
// first version of this check collapsed them and reported "free space
// is not reportable on this platform" on Windows, where it is perfectly
// reportable and there was simply no directory yet to measure.
var ErrNotReportable = errors.New("free space is not reportable on this platform")

// QueueDepths reports what the hub has waiting.
//
// A queue that is deep is not itself wrong; a queue that is deep and
// staying deep is a consumer that has stopped. This reports the depth
// against the bound, because the bound is what turns a backlog into
// loss, and the reactor's queue drops the oldest when it is reached.
func QueueDepths(depths map[string]QueueDepth) Check {
	return Check{
		Name:  "queue depths",
		Roles: []string{RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "queue depths"}
			if len(depths) == 0 {
				res.Status = Skip
				res.Detail = "nothing on this hub keeps a bounded queue"
				res.Remedy = "The reactor is what has one; it is not configured here."
				return res
			}
			names := make([]string, 0, len(depths))
			for name := range depths {
				names = append(names, name)
			}
			sort.Strings(names)

			var full, deep, fine []string
			for _, name := range names {
				q := depths[name]
				line := fmt.Sprintf("%s: %d of %d", name, q.Depth, q.Limit)
				switch {
				case q.Limit > 0 && q.Depth >= q.Limit:
					full = append(full, line)
				case q.Limit > 0 && q.Depth*2 >= q.Limit:
					deep = append(deep, line)
				default:
					fine = append(fine, line)
				}
			}
			switch {
			case len(full) > 0:
				res.Status = Fail
				res.Detail = strings.Join(append(full, deep...), "; ")
				res.Remedy = "A full reactor queue drops the oldest event to make room, and " +
					"the reaction that would have run never does.\nWatch " +
					"`halite_reactor_dropped_total`; raise `queue_depth` or `workers`, or " +
					"find the reaction that is slow."
			case len(deep) > 0:
				res.Status = Warn
				res.Detail = strings.Join(deep, "; ")
				res.Remedy = "Over half full. It drops the oldest when it fills; see " +
					"`halite_reactor_dropped_total` and the `workers` setting."
			default:
				res.Status = Pass
				res.Detail = strings.Join(fine, "; ")
			}
			return res
		},
	}
}

// QueueDepth is one queue's depth against its bound.
type QueueDepth struct {
	Depth int
	Limit int
}

// ExtensionSignatures checks that every extension this node would load
// is signed by a key it trusts.
//
// An unsigned extension is not a broken one — a development node runs
// them deliberately — so the answer depends on whether this node
// requires signatures. Where it does, an unsigned extension will not
// load and the state that calls it fails at apply time, which is a long
// way from here.
func ExtensionSignatures(required bool, extensions []ExtensionTrust) Check {
	return Check{
		Name:  "extension signatures",
		Roles: []string{RoleNode},
		Run: func(context.Context) Result {
			res := Result{Name: "extension signatures"}
			if len(extensions) == 0 {
				res.Status = Skip
				res.Detail = "no extensions are installed"
				res.Remedy = "Extensions live under the extension directory; there are none " +
					"to check."
				return res
			}
			var bad, unsigned, ok []string
			for _, e := range extensions {
				switch {
				case e.Err != nil:
					bad = append(bad, fmt.Sprintf("%s: %v", e.Name, e.Err))
				case !e.Signed:
					unsigned = append(unsigned, e.Name)
				default:
					ok = append(ok, e.Name)
				}
			}
			switch {
			case len(bad) > 0:
				res.Status = Fail
				res.Detail = strings.Join(bad, "; ")
				res.Remedy = "A signature that does not verify is not a missing one: the " +
					"bundle has changed since it was signed, or it was signed by a key this " +
					"node does not trust.\nRe-sign it, or add the key to `extension_trust`."
			case len(unsigned) > 0 && required:
				res.Status = Fail
				res.Detail = fmt.Sprintf("%d unsigned: %s", len(unsigned), strings.Join(unsigned, ", "))
				res.Remedy = "This node requires signatures, so these will not load and any " +
					"state calling them fails at apply time.\nSign them, or clear " +
					"`require_signed_extensions` on a node where that is deliberate."
			case len(unsigned) > 0:
				res.Status = Warn
				res.Detail = fmt.Sprintf("%d unsigned: %s (signatures are not required here)",
					len(unsigned), strings.Join(unsigned, ", "))
				res.Remedy = "They will load. Set `require_signed_extensions` on a managed " +
					"node so that an extension nobody signed cannot run."
			default:
				res.Status = Pass
				res.Detail = fmt.Sprintf("%d signed by a trusted key", len(ok))
			}
			return res
		},
	}
}

// ExtensionTrust is one extension's signature state.
type ExtensionTrust struct {
	Name   string
	Signed bool
	Err    error
}

// roughly renders a duration the way an operator says it.
func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		if d < time.Second {
			return fmt.Sprintf("%dms", d.Milliseconds())
		}
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// bytesOf renders a size in the unit a person would use.
func bytesOf(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// DirsOf is the set of directories a role writes to, for DiskFree.
func DirsOf(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// blocks multiplies a block count by a block size, whatever width the
// platform gave them.
//
// Declared without a build tag so the statfs files can share it. A
// negative count is nonsense and reads as none free rather than
// wrapping to something enormous.
func blocks[C int64 | uint64 | int32 | uint32, S int64 | uint64 | int32 | uint32](count C, size S) uint64 {
	if count <= 0 || size <= 0 {
		return 0
	}
	return uint64(count) * uint64(size)
}

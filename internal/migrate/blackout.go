package migrate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/edlitmus/halite/internal/config"
)

// Salt's blackout, and where it reads it.
//
// Salt checks blackout in two places, the job handler (`salt/minion.py`) // lexicon:allow
// and the scheduler (`salt/utils/schedule.py`), and both read only the
// agent's pillar and then its grains: `opts["pillar"].get(...)` and
// `opts["grains"].get(...)`. Read from Salt 3006.9 and again from the
// tip of Salt's development branch, which is the same. Never the agent's
// configuration file, except that the file's own `grains:` mapping is
// one way of setting a static grain.
//
// halite reads `quiesce` only from the node's configuration (SPEC 9.6,
// DIVERGENCE 5.694), and the compatibility shim converts the Salt key
// from that file -- the one place Salt never read it. So an estate that
// blacks machines out the way Salt's own code expects, through pillar
// or a grain, loses the blackout when it migrates, and nothing said so.
// These findings say so. They convert nothing, because whether a hub's
// pillar should be able to quiesce or lift quiesce on a node changes
// what quiesce guarantees, and that is not this audit's decision
// (issue 780).

// blackoutKeys are Salt's two names. Matched as whole words, so that a
// key like `minion_blackout_reason` is not taken for either. // lexicon:allow
var blackoutKeys = regexp.MustCompile(`\b(minion_blackout(?:_whitelist)?)\b`) // lexicon:allow

// isBlackoutKey says whether a whole key, not a line, is one of them.
func isBlackoutKey(name string) bool {
	return blackoutKeys.FindString(name) == name
}

// blackoutAction is what to do about any of these findings.
const blackoutAction = "halite does not read this from pillar or grains, so these nodes will not be " +
	"quiesced after migration. Set `quiesce: true` (and `quiesce_allowlist`) in the configuration of " +
	"each node this applies to; SPEC 9.6, DIVERGENCE 5.694 and issue 780."

// auditBlackout reports each line of an SLS file that names one of
// Salt's blackout keys. In a pillar file that is the key itself. In a
// state file it is almost always a grain being set, through
// `grains.present` or `grains.setval`, which is the other place Salt
// reads it; it is reported in either case, because a mention that is
// neither is cheap to dismiss and a real one is a node back in
// automation without anybody deciding it should be.
//
// It reads the file as written rather than the body after the renderer
// line, so the line numbers are the file's own.
func auditBlackout(rep *Report, rel string, src []byte, isPillar bool) {
	where := "a state file, which most likely sets it as a grain"
	if isPillar {
		where = "pillar"
	}
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range blackoutKeys.FindAllStringSubmatch(line, -1) {
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			rep.Findings = append(rep.Findings, Finding{
				Category: CatSemantics, Severity: Review, File: rel, Line: i + 1, Subject: m[1],
				Msg:    fmt.Sprintf("Salt reads %q from pillar and grains, and this is %s", m[1], where),
				Action: blackoutAction,
			})
		}
	}
}

// auditConfigBlackout covers the configuration file's two cases: the
// top-level key, which the shim converts although Salt never read it
// there, and a key under `grains:`, which Salt did read and the shim
// leaves as a grain that nothing here consults for quiesce.
func auditConfigBlackout(rep *Report, path string, cfg *config.Config) {
	if grains := cfg.Map("grains"); grains != nil {
		for _, k := range grains.Keys() {
			name, ok := k.(string)
			if !ok || !isBlackoutKey(name) {
				continue
			}
			rep.Findings = append(rep.Findings, Finding{
				Category: CatSemantics, Severity: Review, File: path, Subject: "grains:" + name,
				Msg:    fmt.Sprintf("this file sets %q as a static grain, which Salt reads for blackout", name),
				Action: blackoutAction,
			})
		}
	}
}

// blackoutTranslationNote is added to the shim's finding for the
// top-level key, because "halite calls it quiesce" reads as though the
// node was blacked out in Salt and will be quiesced here. Only the
// second half is true.
func blackoutTranslationNote(salt string) string {
	if !isBlackoutKey(salt) {
		return ""
	}
	return " Salt never read this key from its configuration file, only from pillar and grains, " +
		"so the node it came from was not blacked out by it. Here it does quiesce the node: " +
		"check that is wanted before upgrading."
}

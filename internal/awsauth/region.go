package awsauth

import (
	"fmt"
	"net/url"
	"regexp"
)

// regionGrammar is what an AWS region name looks like.
//
// Every region in the three partitions this package builds endpoints
// for has the shape `<two letters>-<word>[-<word>...]-<number>`:
// `us-east-1`, `eu-central-2`, `ap-southeast-4`, `il-central-1`,
// `us-gov-west-1`, `cn-north-1`, `cn-northwest-1`. The isolated
// partitions' names (`us-iso-east-1`, `us-isob-east-1`) fit it as
// well, which costs nothing: this is a grammar for keeping a host name
// a host name, not a list of regions that exist. A list would be wrong
// the week AWS opens the next one; the grammar is not.
//
// What it is for is what it leaves out. A region is spliced into a host
// name, and lowercase letters, hyphens and digits are the only bytes it
// admits -- no `.`, so it cannot add a label and move the host into
// another domain; no `@`, so it cannot turn the scheme's authority into
// userinfo and name a different host after it; no `:`, so it cannot
// add a port; no `/`, `?` or `#`, so it cannot end the authority early
// and make the real suffix a path or a query. Each of those has been
// demonstrated against the Secrets Manager extension (DIVERGENCE 5.324),
// where the region could come from a node's own grains.
var regionGrammar = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)

// maxRegionLen bounds a region name. The names quoted above are all
// under twenty bytes, so this is generous. Go's regexp is linear-time,
// so the bound is not about backtracking: it is so that a value which
// cannot be a region is refused on its length before anything else is
// done with it.
const maxRegionLen = 32

// ValidRegion reports whether region is safe to place in a host name.
//
// The error quotes at most a bounded prefix of what it was given: the
// value is often a node's, and an error is logged and returned to an
// operator, neither of which should carry an arbitrary amount of
// somebody else's bytes.
func ValidRegion(region string) error {
	if len(region) <= maxRegionLen && regionGrammar.MatchString(region) {
		return nil
	}
	return fmt.Errorf("%s is not an AWS region name (one like us-east-1 or us-gov-west-1)",
		Bounded(region))
}

// Bounded quotes s for an error message, cut to 64 bytes.
func Bounded(s string) string {
	const limit = 64
	if len(s) > limit {
		return fmt.Sprintf("%q (and %d more bytes)", s[:limit], len(s)-limit)
	}
	return fmt.Sprintf("%q", s)
}

// PartitionSuffix is the DNS suffix of a partition's endpoints.
//
// Only the partitions this package knows how to reach are answered.
// An unknown one is refused rather than defaulted to the commercial
// suffix: a partition can arrive in an ARN somebody else wrote, and
// guessing for it is how a request goes somewhere nobody chose.
func PartitionSuffix(partition string) (string, error) {
	switch partition {
	case "", "aws", "aws-us-gov":
		return "amazonaws.com", nil
	case "aws-cn":
		return "amazonaws.com.cn", nil
	}
	return "", fmt.Errorf("%s is not a partition this can reach (aws, aws-us-gov or aws-cn)",
		Bounded(partition))
}

// ServiceURL is `https://<service>.<region>.<suffix>/`, built as a URL
// rather than as a string.
//
// The region is checked against the grammar first. Then, because the
// grammar is the whole of the defence and a defence of one is worth a
// second look, the URL is rendered and parsed back and its host
// compared to the one that was meant: anything that would make a
// parser see a different host than the one assembled here is refused
// even if a later edit loosens the grammar.
func ServiceURL(service, region, suffix string) (*url.URL, error) {
	if err := ValidRegion(region); err != nil {
		return nil, err
	}
	want := service + "." + region + "." + suffix
	u := &url.URL{Scheme: "https", Host: want, Path: "/"}
	back, err := url.Parse(u.String())
	if err != nil || back.Host != want || back.Hostname() != want || back.User != nil {
		return nil, fmt.Errorf("the endpoint for the region %s does not parse back to %s",
			Bounded(region), Bounded(want))
	}
	return u, nil
}

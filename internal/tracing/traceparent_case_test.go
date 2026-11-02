package tracing

import "testing"

// The header is lowercase hexadecimal throughout, and a header that is not
// is not adopted: an uppercase id and a version of `zz` were accepted.
// DIVERGENCE 5.541.
func TestATraceparentThatIsNotLowercaseHexIsRefused(t *testing.T) {
	const tid, sid = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	for header, want := range map[string]bool{
		"00-" + tid + "-" + sid + "-01":                      true,
		"00-0AF7651916CD43DD8448EB211C80319C-" + sid + "-01": false,
		"00-" + tid + "-B7AD6B7169203331-01":                 false,
		"00-" + tid + "-" + sid + "-0A":                      false,
		"zz-" + tid + "-" + sid + "-01":                      false,
		"0A-" + tid + "-" + sid + "-01":                      false,
		"cc-" + tid + "-" + sid + "-01-more":                 true,
		"ff-" + tid + "-" + sid + "-01":                      false,
		"00-" + tid + "-" + sid + "-0g":                      false,
	} {
		if _, ok := ParseTraceParent(header); ok != want {
			t.Errorf("ParseTraceParent(%q) accepted = %v, want %v", header, ok, want)
		}
	}
}

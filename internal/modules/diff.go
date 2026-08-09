package modules

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const diffMaxBytes = 128 * 1024

// lineDiff produces a minimal -/+ line diff between a and b using an LCS
// walk. Good enough for config files; suppressed for large or binary input.
func lineDiff(a, b []byte) string {
	if len(a) > diffMaxBytes || len(b) > diffMaxBytes {
		return "(diff suppressed: content too large)"
	}
	if !utf8.Valid(a) || !utf8.Valid(b) {
		return "(diff suppressed: binary content)"
	}
	al := strings.Split(strings.TrimRight(string(a), "\n"), "\n")
	bl := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	n, m := len(al), len(bl)
	if n*m > 4_000_000 {
		return "(diff suppressed: too many lines)"
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var sb strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			fmt.Fprintf(&sb, "-%s\n", al[i])
			i++
		default:
			fmt.Fprintf(&sb, "+%s\n", bl[j])
			j++
		}
	}
	for ; i < n; i++ {
		fmt.Fprintf(&sb, "-%s\n", al[i])
	}
	for ; j < m; j++ {
		fmt.Fprintf(&sb, "+%s\n", bl[j])
	}
	return strings.TrimRight(sb.String(), "\n")
}

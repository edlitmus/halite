package config

import "strconv"

// This package converts int64 to int without a bound, which is exact where
// int is 64 bits and truncates silently where it is 32. No release target
// is 32-bit (internal/buildpolicy's TestEveryReleaseTargetHasA64BitInt), so
// rather than bound every conversion, a 32-bit build of this package does
// not compile: on one the constant below is negative, and a negative
// constant is not a uint. DIVERGENCE 5.690.
const _ uint = strconv.IntSize - 64

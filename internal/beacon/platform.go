package beacon

import "runtime"

// goos is this build's platform, which decides whether a beacon
// restricted to one may run.
const goos = runtime.GOOS

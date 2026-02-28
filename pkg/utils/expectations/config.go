package expectations

import "time"

// ExpectationTimeout is the default timeout for expectations.
// If expectations are not satisfied within this duration, they will be considered expired.
var ExpectationTimeout = 5 * time.Minute

package python

import "os"

// osReadFile is split into its own file so test harnesses can override
// readFile with an in-memory implementation without re-importing os.
func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

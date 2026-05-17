package web

import "os"

// writeStderr is the single place we actually call os.Stderr.Write from. Split
// out so test builds can patch it (e.g. to direct logs to a test buffer).
var writeStderr = func(p []byte) (int, error) {
	return os.Stderr.Write(p)
}

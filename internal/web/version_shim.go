package web

import "github.com/optimumsage/superkube/internal/version"

// currentVersion exposes the build's superkube version string. Kept as a
// one-line shim so handler files don't all import internal/version.
func currentVersion() string { return version.String() }

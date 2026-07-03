// Package version holds the build version of the claude-share binaries.
//
// Version is the in-repo default used for local builds. Release builds override
// it at link time via -ldflags "-X .../internal/version.Version=<tag>" (see
// .github/workflows/release.yml), so a downloaded binary reports its release
// tag while a `go build` from source reports the value below.
package version

// Version is the current release version.
var Version = "0.1.1"

// Package version holds the build version injected with -ldflags.
package version

// Version is the release identifier. Builds override it with
// -X github.com/zzzbug01/kite/internal/version.Version=...
var Version = "dev"

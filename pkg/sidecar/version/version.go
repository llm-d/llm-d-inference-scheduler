// Package version contains build version information
package version

var (
	// CommitSHA is the git hash of the latest commit in the build.
	CommitSHA string

	// BuildRef is the build ref from the _PULL_BASE_REF from cloud build trigger.
	BuildRef string
)

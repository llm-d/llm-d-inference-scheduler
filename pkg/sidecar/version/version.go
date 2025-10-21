// The version package contains build version information
package version

var (
	// The git hash of the latest commit in the build.
	CommitSHA string

	// The build ref from the _PULL_BASE_REF from cloud build trigger.
	BuildRef string
)


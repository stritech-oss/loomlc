// Package version reports the loomlc build version.
package version

import "runtime/debug"

// Version is the loomlc version. Release builds set it with
// -ldflags "-X github.com/stritech-oss/loomlc/internal/version.Version=<version>";
// it has to be a package variable, initialized to a constant, for the linker to write it.
var Version = "dev"

// String reports the version to print. It prefers what the linker wrote, and falls back to the module
// version Go records, so a binary installed with `go install …/cmd/loomlc@v0.1.0` knows what it is:
// that path builds without ldflags.
func String() string { return choose(Version, moduleVersion()) }

// choose picks between the two, preferring the linker's. A plain `go build` in a checkout reports a
// pseudo-version Go derives from the commit, which is worth printing; it reports "(devel)" when it has
// no version control to read, and that says no more than "dev" does.
func choose(linked, module string) string {
	if linked != "" && linked != "dev" {
		return linked
	}
	if module != "" && module != "(devel)" {
		return module
	}
	return "dev"
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

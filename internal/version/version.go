// Package version reports the loomlc build version.
package version

// Version is the loomlc version. Release builds set it with
// -ldflags "-X github.com/stritech-oss/loomlc/internal/version.Version=<version>";
// it has to be a package variable for the linker to write it.
var Version = "dev"

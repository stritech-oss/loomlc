package version

import "testing"

func TestChoosePrefersTheLinkersVersion(t *testing.T) {
	tests := map[string]struct {
		linked, module, want string
	}{
		"a release build":                 {linked: "v0.1.0", module: "v0.1.0", want: "v0.1.0"},
		"task build in a tagged checkout": {linked: "v0.1.0-3-gabc1234", module: "", want: "v0.1.0-3-gabc1234"},
		"go install at a tag":             {linked: "dev", module: "v0.1.0", want: "v0.1.0"},
		"go build from source":            {linked: "dev", module: "(devel)", want: "dev"},
		"nothing knows anything":          {linked: "", module: "", want: "dev"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := choose(tt.linked, tt.module); got != tt.want {
				t.Errorf("choose(%q, %q) = %q, want %q", tt.linked, tt.module, got, tt.want)
			}
		})
	}
}

// Whatever the build, something printable comes out: `loomlc --version` can't be empty.
func TestStringIsNeverEmpty(t *testing.T) {
	if String() == "" {
		t.Error("String() is empty")
	}
}

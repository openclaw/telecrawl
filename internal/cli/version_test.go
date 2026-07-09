package cli

import "testing"

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name          string
		linkedVersion string
		moduleVersion string
		want          string
	}{
		{name: "release linker flag", linkedVersion: "0.3.3", moduleVersion: "v9.9.9", want: "0.3.3"},
		{name: "go install module", linkedVersion: "dev", moduleVersion: "v0.3.3", want: "0.3.3"},
		{name: "local build", linkedVersion: "dev", moduleVersion: "(devel)", want: "dev"},
		{name: "missing metadata", want: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.linkedVersion, tt.moduleVersion); got != tt.want {
				t.Fatalf("resolveVersion(%q, %q) = %q, want %q", tt.linkedVersion, tt.moduleVersion, got, tt.want)
			}
		})
	}
}

package buildinfo

import "testing"

func TestDefaultsArePresent(t *testing.T) {
	if Version == "" || Source == "" {
		t.Fatal("build information must never be empty")
	}
}

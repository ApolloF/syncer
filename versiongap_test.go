package main

import "testing"

func TestVersionGap(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "v1.4.0"
	for peer, want := range map[string]string{
		"v1.4.0": "", "v1.3.9": "older", "v1.5.0": "newer", "": "older", "dev": "",
	} {
		if got := versionGap(peer); got != want {
			t.Errorf("versionGap(%q) = %q, want %q", peer, got, want)
		}
	}
	version = "dev"
	if got := versionGap("v1.0.0"); got != "" {
		t.Errorf("dev build: versionGap = %q, want none", got)
	}
}

package update

import "testing"

func TestNewer(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"v0.6.0", "v0.5.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0", "v0.99.99", true},
		{"0.6.1", "v0.6.0", true},
		{"v0.6.0", "v0.6.0", false},
		{"v0.5.9", "v0.6.0", false},
		{"v0.7.0-beta", "v0.6.0", true},
		{"v0.7.0", "dev", false},
		{"dev", "v0.1.0", false},
		{"v1.2.3.4", "v1.0.0", false},
		{"", "v1.0.0", false},
	} {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

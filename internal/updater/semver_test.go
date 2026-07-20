package updater

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.0.40", "0.0.40", 0},
		{"0.0.41", "0.0.40", 1},
		{"0.0.40", "0.0.41", -1},
		{"1.0.0", "0.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		// prerelease: a release > prerelease of same numeric
		{"1.0.0", "1.0.0-beta", 1},
		{"1.0.0-beta", "1.0.0", -1},
		// numeric prerelease segments compare numerically (the old bug)
		{"1.0.0-beta.10", "1.0.0-beta.2", 1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0.0-beta.9", "1.0.0-beta.10", -1},
		// fewer segments comes first
		{"1.0.0-beta.2", "1.0.0-beta.2.1", -1},
		{"1.0.0-beta.2.1", "1.0.0-beta.2", 1},
		// numeric < non-numeric
		{"1.0.0-1", "1.0.0-alpha", -1},
		// rc > beta
		{"1.0.0-rc.1", "1.0.0-beta.99", 1},
		// v prefix tolerated
		{"v0.0.41", "0.0.40", 1},
	}
	for _, tc := range tests {
		got := compareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareVersionsDowngradeRejection(t *testing.T) {
	// current=0.0.41, manifest offers 0.0.40 → compareVersions(manifest, current) <= 0 → no update.
	if got := compareVersions("0.0.40", "0.0.41"); got >= 0 {
		t.Fatalf("downgrade manifest should compare <0, got %d", got)
	}
}

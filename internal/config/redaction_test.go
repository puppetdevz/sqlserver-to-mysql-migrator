package config

import "testing"

func TestPasswordMaskNeverDisclosesPrefix(t *testing.T) {
	for _, value := range []string{"x", "xy", "synthetic-test-password"} {
		if got := maskPassword(value); got != "***" {
			t.Errorf("nonempty password mask must be constant, got %q", got)
		}
	}
	if maskPassword("") != "(empty)" {
		t.Fatal("empty password marker changed")
	}
}

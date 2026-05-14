package matcher

import "testing"

func TestTableNameMatcherKeyCaseSensitive(t *testing.T) {
	m := NewTableNameMatcher(true)

	if got := m.Key(" SAMPLE_MAIN_102 "); got != "SAMPLE_MAIN_102" {
		t.Fatalf("Key() = %q, want %q", got, "SAMPLE_MAIN_102")
	}
	if m.Equal("SAMPLE_MAIN_102", "sample_main_102") {
		t.Fatal("Equal() = true, want false for different case in case-sensitive mode")
	}
}

func TestTableNameMatcherKeyCaseInsensitive(t *testing.T) {
	m := NewTableNameMatcher(false)

	if got := m.Key(" sample_main_102 "); got != "SAMPLE_MAIN_102" {
		t.Fatalf("Key() = %q, want %q", got, "SAMPLE_MAIN_102")
	}
	if !m.Equal("SAMPLE_MAIN_102", "sample_main_102") {
		t.Fatal("Equal() = false, want true for different case in case-insensitive mode")
	}
}

func TestTableNameMatcherBuildSet(t *testing.T) {
	m := NewTableNameMatcher(false)

	set := m.BuildSet([]string{" SAMPLE_MAIN_102 ", "agent"})

	if _, ok := set["SAMPLE_MAIN_102"]; !ok {
		t.Fatal("BuildSet() missing SAMPLE_MAIN_102")
	}
	if _, ok := set["AGENT"]; !ok {
		t.Fatal("BuildSet() missing AGENT")
	}
}

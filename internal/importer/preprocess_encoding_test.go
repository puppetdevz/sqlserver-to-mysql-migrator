package importer

import (
	"testing"
	"unicode/utf8"
)

func TestPreprocessRowDecodesGB18030TextToUTF8(t *testing.T) {
	processed := PreprocessRow([]string{"\xB9\xDC\xC0\xED_\xCE\xB4\xCD\xA8\xB9\xFD"})
	got, ok := processed[0].(string)
	if !ok {
		t.Fatalf("processed value type = %T, want string", processed[0])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("processed value is not valid UTF-8: %q", got)
	}
	if got != "管理_未通过" {
		t.Fatalf("processed value = %q, want 管理_未通过", got)
	}
}

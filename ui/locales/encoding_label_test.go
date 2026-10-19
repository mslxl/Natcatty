package locales

import (
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// The names are the identifiers the original selector showed. They are not
// translated, but the catalogs still own them as display strings.
func TestEncodingLabelsPreserveTheOriginalNames(t *testing.T) {
	msgs := In("en")
	want := []string{"UTF-8", "GB18030", "Shift_JIS", "ISO-8859-1"}
	for index, encoding := range domainterminal.SupportedEncodings() {
		if got := EncodingLabel(msgs, encoding); got != want[index] {
			t.Errorf("label %d = %q, want %q", index, got, want[index])
		}
	}
	for _, tag := range Tags() {
		localized := In(tag)
		for index, encoding := range domainterminal.SupportedEncodings() {
			if got := EncodingLabel(localized, encoding); got != want[index] {
				t.Errorf("%s label %d = %q, want %q", tag, index, got, want[index])
			}
		}
	}
}

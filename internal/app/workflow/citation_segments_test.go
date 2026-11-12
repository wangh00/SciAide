package workflow

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCitationQuoteSegmentsPreserveSourceAndBindIdentity(t *testing.T) {
	for _, text := range []string{"Exact short evidence.", strings.Repeat("第一句含 μ、−1.5 和中文。 Second sentence.\r\n", 80), strings.Repeat("x", 1201)} {
		segments := CitationQuoteSourceSegments("[K-A]", text)
		var all strings.Builder
		for _, segment := range segments {
			if !utf8.ValidString(segment.Text) || utf8.RuneCountInString(segment.Text) > 400 {
				t.Fatal("segment split a rune or exceeded its presentation bound")
			}
			all.WriteString(segment.Text)
		}
		if all.String() != text {
			t.Fatal("segment inventory omitted or changed source bytes")
		}
		if CitationQuoteSourceSegments("[K-A]", text)[0].ID != segments[0].ID || CitationQuoteSourceSegments("[K-B]", text)[0].ID == segments[0].ID || CitationQuoteSourceSegments("[K-A]", text+"changed")[0].ID == segments[0].ID {
			t.Fatal("segment identity is unstable or not scoped to exact source")
		}
	}
	if CitationQuoteSourceSegments("", "Source") != nil || CitationQuoteSourceSegments("[K-A]", "") != nil {
		t.Fatal("empty source binding issued a selection menu")
	}
}

package conversation

import (
	"strings"
	"testing"
)

func TestTitleFromMessage(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"  第一行\n\t第二行  ", "第一行 第二行"},
		{strings.Repeat("研", 30), strings.Repeat("研", 30)},
		{strings.Repeat("研", 31), strings.Repeat("研", 30) + "…"},
		{strings.Repeat("😀", 31), strings.Repeat("😀", 30) + "…"},
		{" \t\n", ""},
	} {
		m := Message{Role: RoleUser, Parts: []MessagePart{{Type: "text", Text: tc.text}}}
		if got := TitleFromMessage(m); got != tc.want {
			t.Errorf("title = %q, want %q", got, tc.want)
		}
		m.Internal = true
		if TitleFromMessage(m) != "" {
			t.Fatal("internal prompt named conversation")
		}
	}
	if TitleFromMessage(Message{Role: RoleAssistant, Parts: []MessagePart{{Type: "text", Text: "answer"}}}) != "" {
		t.Fatal("assistant named conversation")
	}
	if TitleFromMessage(Message{Role: RoleUser, Parts: []MessagePart{{Type: "media", Text: "attachment"}}}) != "" {
		t.Fatal("media named conversation")
	}
}

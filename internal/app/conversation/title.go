package conversation

import "strings"

// TitleFromMessage derives a display label, never rewriting the original message.
// An attachment-only message leaves naming pending until the first text question.
func TitleFromMessage(message Message) string {
	if message.Role != RoleUser || message.Internal {
		return ""
	}
	var parts []string
	for _, part := range message.Parts {
		if part.Type == "text" {
			parts = append(parts, part.Text)
		}
	}
	title := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	runes := []rune(title)
	if len(runes) > 30 {
		return strings.TrimSpace(string(runes[:30])) + "…"
	}
	return title
}

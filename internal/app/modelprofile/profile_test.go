package modelprofile

import "testing"

func TestSensitiveCustomHeadersRejected(t *testing.T) {
	for _, name := range []string{"Authorization", "X-Goog-Api-Key", "X-Lab-Token", "X-Client-Secret", "Cookie"} {
		command := SaveCommand{Name: "test", BaseURL: "https://example.test/v1", ModelID: "model", TimeoutSeconds: 60, CustomHeaders: map[string]string{name: "leak"}}
		if err := validateCommand(command); err == nil {
			t.Fatalf("validateCommand() accepted %s header", name)
		}
	}
}

func TestHeaderNewlinesRejected(t *testing.T) {
	command := SaveCommand{Name: "test", BaseURL: "https://example.test/v1", ModelID: "model", TimeoutSeconds: 60, CustomHeaders: map[string]string{"X-Lab": "ok\r\nX-Leak: true"}}
	if err := validateCommand(command); err == nil {
		t.Fatal("validateCommand() accepted CRLF")
	}
}

func TestBaseURLRejectsEmbeddedCredentials(t *testing.T) {
	if err := validateBaseURL("https://user:password@example.test/v1"); err == nil {
		t.Fatal("validateBaseURL accepted credentials")
	}
}

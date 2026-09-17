package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestResearchEvidenceUsesAbstractChunksNotHeaderSnippet(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{`CREATE TABLE documents(document_id TEXT,original_name TEXT,mime_type TEXT)`, `CREATE TABLE chunks(id TEXT,document_id TEXT,attachment_id TEXT,ordinal INTEGER,locator TEXT,title TEXT,content TEXT,content_sha256 TEXT,source_start INTEGER,source_end INTEGER)`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`INSERT INTO documents VALUES('doc','research-study-metadata.md','text/markdown')`)
	texts := []string{"# Trial\n## Bibliographic metadata\nAuthors only.", "\n## Abstract\nMethods: randomized comparison. " + strings.Repeat("Original text. ", 60), "Results: net difference -1.8 kg; 95% CI -4.0 to 0.4; P = 0.11.\n## Source records\nOther source.", "Other metadata must not become evidence."}
	for n, text := range texts {
		h := sha256.Sum256([]byte(text))
		if _, err := db.Exec(`INSERT INTO chunks VALUES(?,?,?,?,?,?,?,?,?,?)`, fmt.Sprint(n), "doc", "attachment", n+1, "lines:1-30", "", text, hex.EncodeToString(h[:]), n*1000, n*1000+len([]rune(text))); err != nil {
			t.Fatal(err)
		}
	}
	index := projectIndex{db: db, version: IndexVersion{ID: "v"}}
	out, err := index.completeResearchEvidence(context.Background(), []Match{{ChunkID: "0", DocumentID: "doc", AttachmentID: "attachment", Name: "research-study-metadata.md", Snippet: "Authors..."}}, SearchOptions{DocumentIDs: []string{"doc"}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || strings.Contains(out[0].Snippet, "Authors") || !strings.Contains(out[1].Snippet, "P = 0.11") || strings.Contains(out[1].Snippet, "Source records") {
		t.Fatalf("%+v", out)
	}
	for _, m := range out {
		c, err := index.EvidenceChunk(context.Background(), m.DocumentID, m.AttachmentID, m.ChunkID)
		if err != nil || !strings.Contains(c.Content, m.Snippet) {
			t.Fatal("not exact source substring", err)
		}
		if m.Title != c.Title || m.Locator != c.Locator || m.SourceStart != c.SourceStart || m.SourceEnd != c.SourceEnd {
			t.Fatal("research evidence changed immutable source metadata")
		}
	}
	db.Exec(`UPDATE chunks SET content='tampered' WHERE id='2'`)
	if _, err := index.completeResearchEvidence(context.Background(), out, SearchOptions{DocumentIDs: []string{"doc"}, Limit: 3}); err == nil {
		t.Fatal("tampered source accepted")
	}
}

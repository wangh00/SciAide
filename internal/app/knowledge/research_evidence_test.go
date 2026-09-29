package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
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

// Optional read-only replay against a user-authorized index. Never rebuilds or
// mutates the running project, and checks the same round-robin selection policy.
func TestBMJComplementaryEvidenceReadOnlyReplay(t *testing.T) {
	path := os.Getenv("SCIAIDE_TEST_BMJ_INDEX")
	if path == "" {
		t.Skip("optional BMJ index")
	}
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(path, "\\", "/")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var doc string
	if err := db.QueryRow(`SELECT document_id FROM documents WHERE original_name='bmj-2023-075847.full.pdf'`).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	index := projectIndex{db: db, version: IndexVersion{ID: "fixture", RetrievalEngine: RetrievalEngine}}
	queries := []string{"walking jogging yoga resistance training depressive symptoms adults network meta-analysis", "results effect estimate confidence credible interval", "methods population eligibility", "limitations certainty risk bias acceptability", "acceptability dropping out dropout odds ratio"}
	groups := [][]Match{}
	for _, query := range queries {
		options := SearchOptions{Query: query, Limit: 20, DocumentIDs: []string{doc}, EvidenceMode: true}
		matches, _, err := index.SearchWithOptions(context.Background(), options, nil)
		if err != nil {
			t.Fatal(err)
		}
		matches, err = index.completeResearchEvidence(context.Background(), matches, options)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, matches)
	}
	seen := map[string]bool{}
	quotes := ""
	count := 0
	for rank := 0; rank < 20; rank++ {
		for _, group := range groups {
			if rank >= len(group) {
				continue
			}
			m := group[rank]
			if seen[m.ChunkID] {
				continue
			}
			seen[m.ChunkID] = true
			if count < 20 {
				quotes += m.Snippet + "\n"
				count++
			}
		}
	}
	for _, term := range []string{"1210", "1047", "643", "−0.62", "−0.55", "−0.49", "CINeMA", "odds ratio 0.55", "0.31 to 0.99", "0.57, 0.35 to 0.94"} {
		if !strings.Contains(quotes, term) {
			t.Errorf("missing actual result %s in selected %d chunks", term, count)
		}
	}
	t.Logf("retrieved %d exact-source chunks including all three modalities and CINeMA", count)
}

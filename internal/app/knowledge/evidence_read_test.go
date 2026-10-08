package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
)

func TestIndexedEvidenceReadsSourceOrderPagesAndRejectsTampering(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{`CREATE TABLE documents(document_id TEXT,original_name TEXT,mime_type TEXT)`, `CREATE TABLE chunks(id TEXT,document_id TEXT,attachment_id TEXT,ordinal INTEGER,locator TEXT,title TEXT,content TEXT,content_sha256 TEXT,source_start INTEGER,source_end INTEGER)`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO documents VALUES('doc','paper.pdf','application/pdf')`); err != nil {
		t.Fatal(err)
	}
	// Insert out of order, with no query words and multiple chunks per page.
	for _, n := range []int{4, 2, 0, 3, 1} {
		text := fmt.Sprintf("Source block %d; unique fixture observation.", n)
		locator := "page:1"
		if n >= 3 {
			locator = "page:2"
		}
		if _, err = db.Exec(`INSERT INTO chunks VALUES(?,?,?,?,?,?,?,?,?,?)`, fmt.Sprint(n), "doc", "a", n, locator, "", text, citation.QuoteSHA256(text), n*100, n*100+len(text)); err != nil {
			t.Fatal(err)
		}
	}
	i := projectIndex{db: db, version: IndexVersion{ID: "index"}}
	page, more, err := i.readEvidencePage(context.Background(), "doc", "a", "page:1", 0)
	if err != nil || len(page) != 2 || !more || page[0].ChunkID != "0" || page[1].ChunkID != "1" || page[0].IndexVersionID != "index" {
		t.Fatalf("page=%+v more=%v err=%v", page, more, err)
	}
	page, more, err = i.readEvidencePage(context.Background(), "doc", "a", "page:1", 2)
	if err != nil || len(page) != 1 || more || page[0].ChunkID != "2" {
		t.Fatal(page, more, err)
	}
	page, more, err = i.readEvidencePage(context.Background(), "doc", "a", "", 4)
	if err != nil || len(page) != 1 || more || page[0].ChunkID != "4" {
		t.Fatal(page, more, err)
	}
	for _, target := range []struct{ a, locator string }{{"a", "page:99"}, {"foreign", "page:1"}} {
		if _, _, err = i.readEvidencePage(context.Background(), "doc", target.a, target.locator, 0); err == nil {
			t.Fatal("foreign/missing source accepted")
		}
	}
	if _, err = db.Exec(`UPDATE chunks SET content='tampered' WHERE id='0'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = i.readEvidencePage(context.Background(), "doc", "a", "", 0); err == nil {
		t.Fatal("altered chunk accepted")
	}
	if _, _, err = (&Service{}).ReadIndexedEvidence(context.Background(), "p", "task", "a", "", -1); err == nil {
		t.Fatal("negative offset accepted")
	}
}

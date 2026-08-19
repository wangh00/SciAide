package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
	_ "modernc.org/sqlite"
)

type projectIndex struct {
	db      *sql.DB
	version IndexVersion
}

const queryEmbeddingCacheLimit = 512

func openProjectIndex(ctx context.Context, selectedProject project.Project, version IndexVersion) (*projectIndex, error) {
	if selectedProject.ID != version.ProjectID {
		return nil, fmt.Errorf("knowledge index does not belong to the current project")
	}
	if err := project.VerifyPrivateDataLayout(selectedProject); err != nil {
		return nil, fmt.Errorf("project knowledge storage is unavailable: %w", err)
	}
	relative := filepath.Clean(filepath.FromSlash(strings.TrimSpace(version.StorageRelativePath)))
	wantedDirectory := filepath.Join("cache", "knowledge")
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || filepath.Dir(relative) != wantedDirectory || filepath.Ext(relative) != ".db" {
		return nil, fmt.Errorf("knowledge index path is invalid")
	}
	privateRoot := project.PrivateDataPath(selectedProject)
	root, err := os.OpenRoot(privateRoot)
	if err != nil {
		return nil, fmt.Errorf("open project data root: %w", err)
	}
	if err := root.MkdirAll(wantedDirectory, 0o700); err != nil {
		root.Close()
		return nil, fmt.Errorf("create project knowledge cache: %w", err)
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	guard, err := pathguard.Open(privateRoot)
	if err != nil {
		return nil, fmt.Errorf("verify project knowledge cache: %w", err)
	}
	defer guard.Close()
	directory, _, err := guard.OpenFile(wantedDirectory)
	if err != nil {
		return nil, err
	}
	directoryInfo, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil || closeErr != nil || !directoryInfo.IsDir() {
		return nil, fmt.Errorf("project knowledge cache is not a regular directory")
	}
	absolute, err := guard.Absolute(relative)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("project knowledge index is not a regular file")
		}
		file, _, err := guard.OpenFile(relative)
		if err != nil {
			return nil, err
		}
		_ = file.Close()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect project knowledge index: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: url.Values{
		"_pragma": []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"},
	}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open project knowledge index: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initializeProjectIndex(ctx, db, selectedProject.ID, version); err != nil {
		db.Close()
		return nil, err
	}
	return &projectIndex{db: db, version: version}, nil
}

func initializeProjectIndex(ctx context.Context, db *sql.DB, projectID string, version IndexVersion) error {
	commonSchema := `
		CREATE TABLE IF NOT EXISTS index_metadata (
			key TEXT PRIMARY KEY NOT NULL,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS documents (
			document_id TEXT PRIMARY KEY NOT NULL,
			attachment_id TEXT NOT NULL UNIQUE,
			attachment_sha256 TEXT NOT NULL,
			original_name TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			document_format TEXT NOT NULL,
			chunk_count INTEGER NOT NULL CHECK (chunk_count >= 0),
			indexed_at TEXT NOT NULL
		);`
	chunkSchema := `
		CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY NOT NULL,
			document_id TEXT NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
			attachment_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK (ordinal > 0),
			unit_index INTEGER NOT NULL CHECK (unit_index > 0),
			kind TEXT NOT NULL,
			locator TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
			UNIQUE(document_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_chunks_attachment ON chunks(attachment_id, ordinal);`
	if version.RetrievalEngine == RetrievalEngine {
		chunkSchema = `
		CREATE TABLE IF NOT EXISTS chunks (
			id TEXT PRIMARY KEY NOT NULL,
			document_id TEXT NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE,
			attachment_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK (ordinal > 0),
			unit_index INTEGER NOT NULL CHECK (unit_index > 0),
			kind TEXT NOT NULL,
			locator TEXT NOT NULL,
			source_start INTEGER NOT NULL CHECK (source_start >= 0),
			source_end INTEGER NOT NULL CHECK (source_end >= source_start),
			title TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
			UNIQUE(document_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_chunks_attachment ON chunks(attachment_id, ordinal);
		CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
			title_terms,
			content_terms,
			content='',
			contentless_delete=1,
			tokenize='unicode61 remove_diacritics 2'
		);`
		if version.SchemaVersion >= 3 {
			chunkSchema += `
		CREATE TABLE IF NOT EXISTS chunk_embeddings (
			chunk_id TEXT PRIMARY KEY NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
			dimensions INTEGER NOT NULL CHECK (dimensions > 0),
			vector BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS query_embedding_cache (
			query_hash TEXT PRIMARY KEY NOT NULL CHECK (length(query_hash) = 64),
			embedding_fingerprint TEXT NOT NULL,
			dimensions INTEGER NOT NULL CHECK (dimensions > 0),
			vector BLOB NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL,
			hit_count INTEGER NOT NULL DEFAULT 0 CHECK (hit_count >= 0)
		);`
		}
	}
	if _, err := db.ExecContext(ctx, commonSchema+chunkSchema); err != nil {
		return fmt.Errorf("initialize project knowledge index: %w", err)
	}
	wanted := map[string]string{
		"project_id":            projectID,
		"index_version_id":      version.ID,
		"schema_version":        fmt.Sprintf("%d", version.SchemaVersion),
		"parser_schema_version": fmt.Sprintf("%d", version.ParserSchemaVersion),
		"chunking_version":      version.ChunkingVersion,
		"search_kind":           version.SearchKind,
		"retrieval_engine":      version.RetrievalEngine,
	}
	if version.SchemaVersion >= 3 {
		wanted["embedding_model"] = version.EmbeddingModel
		wanted["embedding_dimensions"] = fmt.Sprintf("%d", version.EmbeddingDimensions)
		wanted["embedding_config_fingerprint"] = version.EmbeddingFingerprint
		wanted["hybrid_strategy"] = version.HybridStrategy
	}
	for key, value := range wanted {
		var existing string
		err := db.QueryRowContext(ctx, `SELECT value FROM index_metadata WHERE key=?`, key).Scan(&existing)
		if err == sql.ErrNoRows {
			if _, err := db.ExecContext(ctx, `INSERT INTO index_metadata(key,value) VALUES (?,?)`, key, value); err != nil {
				return fmt.Errorf("write project knowledge metadata: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("read project knowledge metadata: %w", err)
		}
		if existing != value {
			return fmt.Errorf("project knowledge index identity does not match its active version")
		}
	}
	return nil
}

func (i *projectIndex) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

func (i *projectIndex) HasAttachment(ctx context.Context, attachmentID, sha256 string) (bool, error) {
	var found int
	query := `SELECT EXISTS(SELECT 1 FROM documents WHERE attachment_id=? AND attachment_sha256=?)`
	if i.version.HybridStrategy == HybridRRF {
		query = `SELECT EXISTS(SELECT 1 FROM documents d WHERE d.attachment_id=? AND d.attachment_sha256=? AND d.chunk_count=(SELECT COUNT(*) FROM chunks c JOIN chunk_embeddings e ON e.chunk_id=c.id WHERE c.document_id=d.document_id))`
	}
	err := i.db.QueryRowContext(ctx, query, strings.TrimSpace(attachmentID), strings.TrimSpace(sha256)).Scan(&found)
	return found == 1, err
}

func (i *projectIndex) ReplaceDocument(ctx context.Context, value attachment.Attachment, documentValue Document, chunks []Chunk, vectors [][]float32, at string) error {
	if value.ProjectID != documentValue.ProjectID || value.ID != documentValue.AttachmentID || documentValue.IndexVersionID != i.version.ID {
		return fmt.Errorf("knowledge document identity is inconsistent")
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project document indexing: %w", err)
	}
	defer tx.Rollback()
	withEmbeddings := i.version.HybridStrategy == HybridRRF
	if withEmbeddings && (i.version.SchemaVersion < 3 || i.version.EmbeddingDimensions < 1 || len(vectors) != len(chunks)) {
		return fmt.Errorf("knowledge document embeddings do not match the active index version")
	}
	if i.version.RetrievalEngine == RetrievalEngine {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE rowid IN (SELECT rowid FROM chunks WHERE attachment_id=? OR document_id=?)`, value.ID, documentValue.ID); err != nil {
			return fmt.Errorf("remove prior project full-text entries: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE attachment_id=? OR document_id=?`, value.ID, documentValue.ID); err != nil {
		return fmt.Errorf("remove prior project document index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO documents(document_id,attachment_id,attachment_sha256,original_name,mime_type,document_format,chunk_count,indexed_at) VALUES (?,?,?,?,?,?,?,?)`,
		documentValue.ID, value.ID, value.SHA256, value.OriginalName, value.MIMEType, value.Format, len(chunks), at); err != nil {
		return fmt.Errorf("insert project document index: %w", err)
	}
	insertSQL := `INSERT INTO chunks(id,document_id,attachment_id,ordinal,unit_index,kind,locator,title,content,content_sha256) VALUES (?,?,?,?,?,?,?,?,?,?)`
	if i.version.RetrievalEngine == RetrievalEngine {
		insertSQL = `INSERT INTO chunks(id,document_id,attachment_id,ordinal,unit_index,kind,locator,source_start,source_end,title,content,content_sha256) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	}
	statement, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare project chunks: %w", err)
	}
	defer statement.Close()
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if chunk.DocumentID != documentValue.ID || chunk.AttachmentID != value.ID {
			return fmt.Errorf("knowledge chunk identity is inconsistent")
		}
		arguments := []any{chunk.ID, chunk.DocumentID, chunk.AttachmentID, chunk.Ordinal, chunk.UnitIndex, chunk.Kind, chunk.Locator, chunk.Title, chunk.Content, chunk.ContentSHA256}
		if i.version.RetrievalEngine == RetrievalEngine {
			arguments = []any{chunk.ID, chunk.DocumentID, chunk.AttachmentID, chunk.Ordinal, chunk.UnitIndex, chunk.Kind, chunk.Locator, chunk.SourceStart, chunk.SourceEnd, chunk.Title, chunk.Content, chunk.ContentSHA256}
		}
		inserted, err := statement.ExecContext(ctx, arguments...)
		if err != nil {
			return fmt.Errorf("insert project knowledge chunk: %w", err)
		}
		if i.version.RetrievalEngine == RetrievalEngine {
			rowID, err := inserted.LastInsertId()
			if err != nil {
				return fmt.Errorf("read project knowledge chunk row id: %w", err)
			}
			titleTerms := strings.Join(normalizedTerms(chunk.Title), " ")
			contentTerms := strings.Join(normalizedTerms(chunk.Content), " ")
			if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(rowid,title_terms,content_terms) VALUES (?,?,?)`, rowID, titleTerms, contentTerms); err != nil {
				return fmt.Errorf("insert project full-text terms: %w", err)
			}
		}
	}
	if withEmbeddings {
		statement, err := tx.PrepareContext(ctx, `INSERT INTO chunk_embeddings(chunk_id,dimensions,vector) VALUES (?,?,?)`)
		if err != nil {
			return fmt.Errorf("prepare project embeddings: %w", err)
		}
		defer statement.Close()
		for index, chunk := range chunks {
			if len(vectors[index]) != i.version.EmbeddingDimensions {
				return fmt.Errorf("knowledge embedding dimension mismatch")
			}
			if _, err := statement.ExecContext(ctx, chunk.ID, len(vectors[index]), encodeVector(vectors[index])); err != nil {
				return fmt.Errorf("insert project embedding: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project document index: %w", err)
	}
	return nil
}

func (i *projectIndex) RemoveDocument(ctx context.Context, documentID, attachmentID string) error {
	documentID, attachmentID = strings.TrimSpace(documentID), strings.TrimSpace(attachmentID)
	if documentID == "" || attachmentID == "" {
		return fmt.Errorf("knowledge document identity is required")
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project document removal: %w", err)
	}
	defer tx.Rollback()
	if i.version.RetrievalEngine == RetrievalEngine {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE rowid IN (SELECT rowid FROM chunks WHERE document_id=? OR attachment_id=?)`, documentID, attachmentID); err != nil {
			return fmt.Errorf("remove project full-text entries: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE document_id=? OR attachment_id=?`, documentID, attachmentID); err != nil {
		return fmt.Errorf("remove project document index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project document removal: %w", err)
	}
	return nil
}

func (i *projectIndex) Search(ctx context.Context, query string, limit int) ([]Match, int, error) {
	return i.SearchWithOptions(ctx, SearchOptions{Query: query, Limit: limit}, nil)
}

func (i *projectIndex) CachedQueryVector(ctx context.Context, query string, at time.Time) ([]float32, bool, error) {
	if i.version.HybridStrategy != HybridRRF || i.version.EmbeddingFingerprint == "" || i.version.EmbeddingDimensions < 1 {
		return nil, false, nil
	}
	hash := queryEmbeddingHash(i.version.EmbeddingFingerprint, query)
	var fingerprint string
	var dimensions int
	var encoded []byte
	err := i.db.QueryRowContext(ctx, `SELECT embedding_fingerprint,dimensions,vector FROM query_embedding_cache WHERE query_hash=?`, hash).Scan(&fingerprint, &dimensions, &encoded)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read cached query embedding: %w", err)
	}
	if fingerprint != i.version.EmbeddingFingerprint || dimensions != i.version.EmbeddingDimensions {
		_, _ = i.db.ExecContext(ctx, `DELETE FROM query_embedding_cache WHERE query_hash=?`, hash)
		return nil, false, nil
	}
	vector, err := decodeVector(encoded, dimensions)
	if err != nil {
		_, _ = i.db.ExecContext(ctx, `DELETE FROM query_embedding_cache WHERE query_hash=?`, hash)
		return nil, false, nil
	}
	if _, err := i.db.ExecContext(ctx, `UPDATE query_embedding_cache SET last_used_at=?,hit_count=hit_count+1 WHERE query_hash=?`, at.UTC().Format(time.RFC3339Nano), hash); err != nil {
		return nil, false, fmt.Errorf("touch cached query embedding: %w", err)
	}
	return vector, true, nil
}

func (i *projectIndex) StoreQueryVector(ctx context.Context, query string, vector []float32, at time.Time) error {
	if i.version.HybridStrategy != HybridRRF || i.version.EmbeddingFingerprint == "" || len(vector) != i.version.EmbeddingDimensions {
		return fmt.Errorf("query embedding does not match the active knowledge index")
	}
	hash := queryEmbeddingHash(i.version.EmbeddingFingerprint, query)
	timestamp := at.UTC().Format(time.RFC3339Nano)
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin query embedding cache write: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO query_embedding_cache(query_hash,embedding_fingerprint,dimensions,vector,created_at,last_used_at,hit_count) VALUES (?,?,?,?,?,?,0)
		ON CONFLICT(query_hash) DO UPDATE SET embedding_fingerprint=excluded.embedding_fingerprint,dimensions=excluded.dimensions,vector=excluded.vector,last_used_at=excluded.last_used_at`,
		hash, i.version.EmbeddingFingerprint, len(vector), encodeVector(vector), timestamp, timestamp); err != nil {
		return fmt.Errorf("store query embedding cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM query_embedding_cache WHERE query_hash IN (
		SELECT query_hash FROM query_embedding_cache ORDER BY last_used_at DESC,query_hash LIMIT -1 OFFSET ?
	)`, queryEmbeddingCacheLimit); err != nil {
		return fmt.Errorf("prune query embedding cache: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit query embedding cache: %w", err)
	}
	return nil
}

func queryEmbeddingHash(fingerprint, query string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(fingerprint) + "\x00" + strings.TrimSpace(query)))
	return hex.EncodeToString(digest[:])
}

func (i *projectIndex) SearchWithOptions(ctx context.Context, options SearchOptions, queryVector []float32) ([]Match, int, error) {
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" || options.Limit < 1 {
		return nil, 0, fmt.Errorf("knowledge search query and limit are required")
	}
	candidateLimit := min(160, max(40, options.Limit*8))
	var lexical []Match
	var lexicalTotal int
	var err error
	if i.version.RetrievalEngine == RetrievalEngine {
		if len([]rune(options.Query)) < 2 {
			lexical, lexicalTotal, err = i.searchLexical(ctx, options.Query, candidateLimit, options)
		} else {
			lexical, lexicalTotal, err = i.searchFTS(ctx, options.Query, candidateLimit, options)
		}
	} else {
		lexical, lexicalTotal, err = i.searchLexical(ctx, options.Query, candidateLimit, options)
	}
	if err != nil {
		return nil, 0, err
	}
	lexical = filterMatches(lexical, options)
	if i.version.HybridStrategy != HybridRRF || len(queryVector) == 0 {
		if len(lexical) > options.Limit {
			lexical = lexical[:options.Limit]
		}
		for index := range lexical {
			lexical[index].Rank = index + 1
			lexical[index].LexicalRank = index + 1
		}
		return lexical, lexicalTotal, nil
	}
	if len(queryVector) != i.version.EmbeddingDimensions {
		return nil, 0, fmt.Errorf("query embedding dimension does not match the knowledge index")
	}
	semantic, err := i.searchVector(ctx, options.Query, queryVector, candidateLimit, options)
	if err != nil {
		return nil, 0, err
	}
	semantic = filterMatches(semantic, options)
	matches := fuseHybrid(lexical, semantic, options.Limit)
	return matches, len(uniqueMatchIDs(lexical, semantic)), nil

}

func (i *projectIndex) searchLexical(ctx context.Context, query string, limit int, filters ...SearchOptions) ([]Match, int, error) {
	withSourceSpan := i.version.RetrievalEngine == RetrievalEngine
	querySQL := `SELECT c.id,c.document_id,c.attachment_id,d.original_name,d.mime_type,d.document_format,c.locator,c.title,c.content FROM chunks c JOIN documents d ON d.document_id=c.document_id`
	if withSourceSpan {
		querySQL = `SELECT c.id,c.document_id,c.attachment_id,d.original_name,d.mime_type,d.document_format,c.locator,c.source_start,c.source_end,c.title,c.content FROM chunks c JOIN documents d ON d.document_id=c.document_id`
	}
	options := SearchOptions{}
	if len(filters) > 0 {
		options = filters[0]
	}
	filterSQL, filterArgs := searchFilterClause(options)
	querySQL += " WHERE 1=1" + filterSQL + " ORDER BY d.original_name,c.ordinal"
	rows, err := i.db.QueryContext(ctx, querySQL, filterArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("search project knowledge index: %w", err)
	}
	defer rows.Close()
	matches := make([]Match, 0)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		var item Match
		var content string
		destinations := []any{&item.ChunkID, &item.DocumentID, &item.AttachmentID, &item.Name, &item.MIMEType, &item.Format, &item.Locator, &item.Title, &content}
		if withSourceSpan {
			destinations = []any{&item.ChunkID, &item.DocumentID, &item.AttachmentID, &item.Name, &item.MIMEType, &item.Format, &item.Locator, &item.SourceStart, &item.SourceEnd, &item.Title, &content}
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, 0, err
		}
		score, position, matched := lexicalScore(content, item.Title, query)
		if !matched {
			continue
		}
		item.Score = score
		item.Snippet = searchSnippet(content, position, len([]rune(query)))
		if !withSourceSpan {
			item.SourceEnd = len([]rune(content))
		}
		item.MatchedTerms = matchingTerms(content+" "+item.Title, uniqueQueryTerms(query))
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].Score != matches[right].Score {
			return matches[left].Score > matches[right].Score
		}
		if matches[left].Name != matches[right].Name {
			return matches[left].Name < matches[right].Name
		}
		return matches[left].Locator < matches[right].Locator
	})
	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	for index := range matches {
		matches[index].Rank = index + 1
	}
	return matches, total, nil
}

func (i *projectIndex) searchFTS(ctx context.Context, query string, limit int, filters ...SearchOptions) ([]Match, int, error) {
	terms := uniqueQueryTerms(query)
	if len(terms) == 0 {
		return i.searchLexical(ctx, query, limit, filters...)
	}
	options := SearchOptions{}
	if len(filters) > 0 {
		options = filters[0]
	}
	filterSQL, filterArgs := searchFilterClause(options)
	matchQuery := ftsQuery(terms)
	var total int
	countArgs := append([]any{matchQuery}, filterArgs...)
	if err := i.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks_fts JOIN chunks c ON c.rowid=chunks_fts.rowid JOIN documents d ON d.document_id=c.document_id WHERE chunks_fts MATCH ?`+filterSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count project full-text matches: %w", err)
	}
	candidateLimit := min(160, max(40, limit*8))
	queryArgs := append([]any{matchQuery}, filterArgs...)
	queryArgs = append(queryArgs, candidateLimit)
	rows, err := i.db.QueryContext(ctx, `SELECT c.id,c.document_id,c.attachment_id,d.original_name,d.mime_type,d.document_format,c.locator,c.source_start,c.source_end,c.title,c.content,bm25(chunks_fts,6.0,1.0)
		FROM chunks_fts JOIN chunks c ON c.rowid=chunks_fts.rowid JOIN documents d ON d.document_id=c.document_id
		WHERE chunks_fts MATCH ?`+filterSQL+` ORDER BY bm25(chunks_fts,6.0,1.0),d.original_name,c.ordinal LIMIT ?`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("search project FTS5 index: %w", err)
	}
	defer rows.Close()
	candidates := make([]Match, 0, candidateLimit)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		var item Match
		var content string
		var bm25Value float64
		if err := rows.Scan(&item.ChunkID, &item.DocumentID, &item.AttachmentID, &item.Name, &item.MIMEType, &item.Format, &item.Locator, &item.SourceStart, &item.SourceEnd, &item.Title, &content, &bm25Value); err != nil {
			return nil, 0, err
		}
		exactScore, position, _ := lexicalScore(content, item.Title, query)
		if position < 0 {
			position = firstTermPosition(content, terms)
		}
		item.Score = (-bm25Value * 1_000_000) + exactScore
		item.MatchedTerms = matchingTerms(content+" "+item.Title, terms)
		item.Snippet = searchSnippet(content, position, len([]rune(query)))
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		if candidates[left].Name != candidates[right].Name {
			return candidates[left].Name < candidates[right].Name
		}
		return candidates[left].SourceStart < candidates[right].SourceStart
	})
	perDocument := make(map[string]int)
	matches := make([]Match, 0, limit)
	for _, candidate := range candidates {
		if perDocument[candidate.DocumentID] >= 3 {
			continue
		}
		perDocument[candidate.DocumentID]++
		candidate.Rank = len(matches) + 1
		matches = append(matches, candidate)
		if len(matches) == limit {
			break
		}
	}
	return matches, total, nil
}

func (i *projectIndex) searchVector(ctx context.Context, query string, queryVector []float32, limit int, options SearchOptions) ([]Match, error) {
	filterSQL, filterArgs := searchFilterClause(options)
	rows, err := i.db.QueryContext(ctx, `SELECT c.id,c.document_id,c.attachment_id,d.original_name,d.mime_type,d.document_format,c.locator,c.source_start,c.source_end,c.title,c.content,e.dimensions,e.vector
		FROM chunk_embeddings e JOIN chunks c ON c.id=e.chunk_id JOIN documents d ON d.document_id=c.document_id WHERE 1=1`+filterSQL+` ORDER BY d.original_name,c.ordinal`, filterArgs...)
	if err != nil {
		return nil, fmt.Errorf("search project vector index: %w", err)
	}
	defer rows.Close()
	terms := uniqueQueryTerms(query)
	candidates := make([]Match, 0)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var item Match
		var content string
		var dimensions int
		var encoded []byte
		if err := rows.Scan(&item.ChunkID, &item.DocumentID, &item.AttachmentID, &item.Name, &item.MIMEType, &item.Format, &item.Locator, &item.SourceStart, &item.SourceEnd, &item.Title, &content, &dimensions, &encoded); err != nil {
			return nil, err
		}
		if dimensions != len(queryVector) {
			return nil, fmt.Errorf("stored knowledge embedding dimension mismatch")
		}
		vector, err := decodeVector(encoded, dimensions)
		if err != nil {
			return nil, err
		}
		item.SemanticScore = cosine(queryVector, vector)
		item.Score = item.SemanticScore
		item.MatchedTerms = matchingTerms(content+" "+item.Title, terms)
		item.Snippet = searchSnippet(content, firstTermPosition(content, terms), len([]rune(query)))
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].SemanticScore != candidates[right].SemanticScore {
			return candidates[left].SemanticScore > candidates[right].SemanticScore
		}
		return candidates[left].ChunkID < candidates[right].ChunkID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for index := range candidates {
		candidates[index].SemanticRank = index + 1
	}
	return candidates, nil
}

func filterMatches(values []Match, options SearchOptions) []Match {
	documents := make(map[string]struct{}, len(options.DocumentIDs))
	for _, value := range options.DocumentIDs {
		if value = strings.TrimSpace(value); value != "" {
			documents[value] = struct{}{}
		}
	}
	formats := make(map[document.Format]struct{}, len(options.Formats))
	for _, value := range options.Formats {
		formats[value] = struct{}{}
	}
	if len(documents) == 0 && len(formats) == 0 {
		return values
	}
	result := make([]Match, 0, len(values))
	for _, value := range values {
		if len(documents) > 0 {
			if _, ok := documents[value.DocumentID]; !ok {
				continue
			}
		}
		if len(formats) > 0 {
			if _, ok := formats[value.Format]; !ok {
				continue
			}
		}
		result = append(result, value)
	}
	return result
}

func searchFilterClause(options SearchOptions) (string, []any) {
	clauses := make([]string, 0, 2)
	arguments := make([]any, 0, len(options.DocumentIDs)+len(options.Formats))
	if len(options.DocumentIDs) > 0 {
		placeholders := make([]string, 0, len(options.DocumentIDs))
		for _, value := range options.DocumentIDs {
			if value = strings.TrimSpace(value); value != "" {
				placeholders = append(placeholders, "?")
				arguments = append(arguments, value)
			}
		}
		if len(placeholders) > 0 {
			clauses = append(clauses, "c.document_id IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if len(options.Formats) > 0 {
		placeholders := make([]string, len(options.Formats))
		for index, value := range options.Formats {
			placeholders[index] = "?"
			arguments = append(arguments, value)
		}
		clauses = append(clauses, "d.document_format IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(clauses) == 0 {
		return "", arguments
	}
	return " AND " + strings.Join(clauses, " AND "), arguments
}

func fuseHybrid(lexical, semantic []Match, limit int) []Match {
	type fused struct {
		match Match
		score float64
	}
	byID := make(map[string]*fused, len(lexical)+len(semantic))
	for rank, value := range lexical {
		value.LexicalRank = rank + 1
		item := &fused{match: value, score: 1 / float64(60+rank+1)}
		byID[value.ChunkID] = item
	}
	for rank, value := range semantic {
		item := byID[value.ChunkID]
		if item == nil {
			copyValue := value
			item = &fused{match: copyValue}
			byID[value.ChunkID] = item
		} else {
			item.match.SemanticScore = value.SemanticScore
			item.match.SemanticRank = rank + 1
			item.match.MatchedTerms = mergeTerms(item.match.MatchedTerms, value.MatchedTerms)
		}
		item.match.SemanticRank = rank + 1
		item.score += 1 / float64(60+rank+1)
	}
	values := make([]fused, 0, len(byID))
	for _, value := range byID {
		value.match.Score = value.score
		values = append(values, *value)
	}
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].score != values[right].score {
			return values[left].score > values[right].score
		}
		return values[left].match.ChunkID < values[right].match.ChunkID
	})
	perDocument := make(map[string]int)
	result := make([]Match, 0, limit)
	for _, value := range values {
		if perDocument[value.match.DocumentID] >= 3 || overlapsSelected(result, value.match) {
			continue
		}
		perDocument[value.match.DocumentID]++
		value.match.Rank = len(result) + 1
		result = append(result, value.match)
		if len(result) == limit {
			break
		}
	}
	return result
}

func overlapsSelected(values []Match, candidate Match) bool {
	for _, value := range values {
		if value.DocumentID != candidate.DocumentID || value.Locator != candidate.Locator {
			continue
		}
		intersection := min(value.SourceEnd, candidate.SourceEnd) - max(value.SourceStart, candidate.SourceStart)
		shorter := min(value.SourceEnd-value.SourceStart, candidate.SourceEnd-candidate.SourceStart)
		if intersection > 0 && shorter > 0 && intersection*2 >= shorter {
			return true
		}
	}
	return false
}

func mergeTerms(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	result := make([]string, 0, min(12, len(left)+len(right)))
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
			if len(result) == 12 {
				return result
			}
		}
	}
	return result
}

func uniqueMatchIDs(groups ...[]Match) map[string]struct{} {
	result := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			result[value.ChunkID] = struct{}{}
		}
	}
	return result
}

func encodeVector(vector []float32) []byte {
	result := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(result[index*4:], math.Float32bits(value))
	}
	return result
}

func decodeVector(value []byte, dimensions int) ([]float32, error) {
	if dimensions < 1 || len(value) != dimensions*4 {
		return nil, fmt.Errorf("stored knowledge embedding is invalid")
	}
	result := make([]float32, dimensions)
	for index := range result {
		result[index] = math.Float32frombits(binary.LittleEndian.Uint32(value[index*4:]))
	}
	return result, nil
}

func cosine(left, right []float32) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return -1
	}
	var value float64
	for index := range left {
		value += float64(left[index] * right[index])
	}
	return value
}

func (i *projectIndex) Optimize(ctx context.Context) error {
	if i.version.RetrievalEngine != RetrievalEngine {
		return nil
	}
	if _, err := i.db.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES ('optimize')`); err != nil {
		return fmt.Errorf("optimize project full-text index: %w", err)
	}
	if _, err := i.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint project full-text index: %w", err)
	}
	return nil
}

func matchingTerms(value string, terms []string) []string {
	available := make(map[string]struct{})
	for _, term := range normalizedTerms(value) {
		available[term] = struct{}{}
	}
	result := make([]string, 0, min(len(terms), 12))
	for _, term := range terms {
		if _, matched := available[term]; matched {
			result = append(result, term)
			if len(result) == 12 {
				break
			}
		}
	}
	return result
}

func firstTermPosition(value string, terms []string) int {
	lowered := lowerRunes(value)
	for _, term := range terms {
		if position := runeIndex(lowered, lowerRunes(term)); position >= 0 {
			return position
		}
	}
	return 0
}

func lexicalScore(content, title, query string) (float64, int, bool) {
	contentRunes := lowerRunes(content)
	queryRunes := lowerRunes(strings.TrimSpace(query))
	position := runeIndex(contentRunes, queryRunes)
	score := 0.0
	if position >= 0 {
		score += 100 + float64(runeOccurrences(contentRunes, queryRunes))*8
	}
	matchedTokens := 0
	seen := map[string]struct{}{}
	for _, token := range strings.Fields(strings.TrimSpace(query)) {
		key := string(lowerRunes(token))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tokenRunes := []rune(key)
		found := runeIndex(contentRunes, tokenRunes)
		if found < 0 {
			continue
		}
		if position < 0 {
			position = found
		}
		matchedTokens++
		score += 12 + float64(min(runeOccurrences(contentRunes, tokenRunes), 8))*2
	}
	if position < 0 {
		return 0, 0, false
	}
	if len(seen) > 0 {
		score += 20 * float64(matchedTokens) / float64(len(seen))
	}
	titleRunes := lowerRunes(title)
	if runeIndex(titleRunes, queryRunes) >= 0 {
		score += 30
	}
	return score, position, true
}

func lowerRunes(value string) []rune {
	result := []rune(value)
	for index, value := range result {
		result[index] = unicode.ToLower(value)
	}
	return result
}

func runeIndex(value, query []rune) int {
	if len(query) == 0 || len(query) > len(value) {
		return -1
	}
	for index := 0; index+len(query) <= len(value); index++ {
		matched := true
		for offset := range query {
			if value[index+offset] != query[offset] {
				matched = false
				break
			}
		}
		if matched {
			return index
		}
	}
	return -1
}

func runeOccurrences(value, query []rune) int {
	if len(query) == 0 {
		return 0
	}
	count := 0
	for offset := 0; offset+len(query) <= len(value); {
		index := runeIndex(value[offset:], query)
		if index < 0 {
			break
		}
		count++
		offset += index + len(query)
	}
	return count
}

func searchSnippet(content string, position, queryLength int) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) == 0 {
		return ""
	}
	position = max(0, min(position, len(runes)))
	start := max(0, position-240)
	end := min(len(runes), position+max(queryLength, 1)+420)
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(runes) {
		suffix = "..."
	}
	return prefix + strings.TrimSpace(string(runes[start:end])) + suffix
}

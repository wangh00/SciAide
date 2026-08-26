package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/project"
)

type ReviewStatus string

const (
	ReviewPending  ReviewStatus = "pending"
	ReviewIncluded ReviewStatus = "included"
	ReviewExcluded ReviewStatus = "excluded"
)

type ImportStatus string

const (
	ImportNotImported ImportStatus = "not_imported"
	ImportImporting   ImportStatus = "importing"
	ImportImported    ImportStatus = "imported"
	ImportFailed      ImportStatus = "failed"
)

type ImportKind string

const (
	ImportFullText         ImportKind = "full_text"
	ImportMetadataAbstract ImportKind = "metadata_abstract"
)

type Query struct {
	ID             string         `json:"id"`
	ProjectID      string         `json:"projectId"`
	Text           string         `json:"text"`
	SourceIDs      []string       `json:"sourceIds"`
	LimitPerSource int            `json:"limitPerSource"`
	Sources        []SourceSearch `json:"sources"`
	Partial        bool           `json:"partial"`
	ResultCount    int            `json:"resultCount"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

type SourceRecord struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Work        Work      `json:"work"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CandidateAlias struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Candidate struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"projectId"`
	CandidateKey    string           `json:"candidateKey"`
	ReviewStatus    ReviewStatus     `json:"reviewStatus"`
	ExclusionReason string           `json:"exclusionReason,omitempty"`
	Note            string           `json:"note,omitempty"`
	ImportStatus    ImportStatus     `json:"importStatus"`
	ImportKind      ImportKind       `json:"importKind,omitempty"`
	AttachmentID    string           `json:"attachmentId,omitempty"`
	ImportError     string           `json:"importError,omitempty"`
	Preferred       Work             `json:"preferred"`
	Records         []SourceRecord   `json:"records"`
	Aliases         []CandidateAlias `json:"aliases"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
}

type DiscoverySearchCommand struct {
	ProjectID string   `json:"projectId"`
	Query     string   `json:"query"`
	SourceIDs []string `json:"sourceIds,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type CandidateListCommand struct {
	ProjectID string       `json:"projectId"`
	QueryID   string       `json:"queryId,omitempty"`
	Status    ReviewStatus `json:"status,omitempty"`
	Search    string       `json:"search,omitempty"`
	Sort      string       `json:"sort,omitempty"`
	Offset    int          `json:"offset,omitempty"`
	Limit     int          `json:"limit,omitempty"`
}

type CandidatePage struct {
	Items  []Candidate `json:"items"`
	Total  int         `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
}

type DiscoverySearchResult struct {
	Query Query         `json:"query"`
	Page  CandidatePage `json:"page"`
}

type ReviewCommand struct {
	ProjectID       string       `json:"projectId"`
	CandidateID     string       `json:"candidateId"`
	Status          ReviewStatus `json:"status"`
	ExclusionReason string       `json:"exclusionReason,omitempty"`
	Note            string       `json:"note,omitempty"`
}

type ImportStateCommand struct {
	ProjectID    string
	CandidateID  string
	Status       ImportStatus
	Kind         ImportKind
	AttachmentID string
	ErrorMessage string
	At           time.Time
}

type DiscoveryRepository interface {
	SaveSearch(ctx context.Context, projectID, queryKey string, command SearchCommand, result SearchResult, at time.Time) (Query, error)
	ListQueries(ctx context.Context, projectID string, limit int) ([]Query, error)
	ListCandidates(ctx context.Context, projectID, queryID string) ([]Candidate, error)
	GetCandidate(ctx context.Context, projectID, candidateID string) (Candidate, error)
	UpdateReview(ctx context.Context, command ReviewCommand, at time.Time) (Candidate, error)
	UpdateImportState(ctx context.Context, command ImportStateCommand) (Candidate, error)
	RecoverImports(ctx context.Context, at time.Time) (int64, error)
}

type DiscoveryProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type MaterializeMode string

const (
	MaterializeAuto     MaterializeMode = "auto"
	MaterializeFullText MaterializeMode = "full_text"
	MaterializeMetadata MaterializeMode = "metadata_abstract"
)

type MaterializedCandidate struct {
	Path   string
	Name   string
	SHA256 string
	Kind   ImportKind
}

type CandidateMaterializer interface {
	Materialize(ctx context.Context, selected project.Project, candidate Candidate, mode MaterializeMode) (MaterializedCandidate, error)
	Cleanup(value MaterializedCandidate)
}

type ResearchAttachmentImporter interface {
	ImportResearchStaged(ctx context.Context, projectID, path, name string) (attachment.Attachment, error)
}

type ResearchKnowledgeImporter interface {
	Enqueue(ctx context.Context, value attachment.Attachment) error
}

type ImportCandidateCommand struct {
	ProjectID   string          `json:"projectId"`
	CandidateID string          `json:"candidateId"`
	Mode        MaterializeMode `json:"mode,omitempty"`
}

type ImportCandidateResult struct {
	Candidate  Candidate             `json:"candidate"`
	Attachment attachment.Attachment `json:"attachment"`
}

type DiscoveryService struct {
	research     *Service
	repository   DiscoveryRepository
	projects     DiscoveryProjectLoader
	materializer CandidateMaterializer
	attachments  ResearchAttachmentImporter
	knowledge    ResearchKnowledgeImporter
	now          func() time.Time
}

func (s *DiscoveryService) SetImportPipeline(materializer CandidateMaterializer, attachments ResearchAttachmentImporter, knowledge ResearchKnowledgeImporter) error {
	if materializer == nil || attachments == nil || knowledge == nil {
		return fmt.Errorf("research import pipeline dependencies are required")
	}
	s.materializer, s.attachments, s.knowledge = materializer, attachments, knowledge
	return nil
}

func (s *DiscoveryService) Recover(ctx context.Context) (int64, error) {
	return s.repository.RecoverImports(ctx, s.now())
}

func NewDiscoveryService(research *Service, repository DiscoveryRepository, projects DiscoveryProjectLoader) (*DiscoveryService, error) {
	if research == nil || repository == nil || projects == nil {
		return nil, fmt.Errorf("research discovery dependencies are required")
	}
	return &DiscoveryService{research: research, repository: repository, projects: projects, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *DiscoveryService) Catalog() []Source { return s.research.Catalog() }

func (s *DiscoveryService) Search(ctx context.Context, command DiscoverySearchCommand) (DiscoverySearchResult, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return DiscoverySearchResult{}, err
	}
	limit := command.Limit
	if limit == 0 {
		limit = 20
	}
	result, err := s.research.Search(ctx, SearchCommand{Query: command.Query, SourceIDs: command.SourceIDs, Limit: limit})
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	effectiveSourceIDs := append([]string(nil), command.SourceIDs...)
	if len(effectiveSourceIDs) == 0 {
		for _, status := range result.Sources {
			effectiveSourceIDs = append(effectiveSourceIDs, status.SourceID)
		}
	}
	key := SearchQueryKey(result.Query, effectiveSourceIDs, limit)
	query, err := s.repository.SaveSearch(ctx, projectID, key, SearchCommand{Query: result.Query, SourceIDs: effectiveSourceIDs, Limit: limit}, result, s.now())
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	page, err := s.ListCandidates(ctx, CandidateListCommand{ProjectID: projectID, QueryID: query.ID, Sort: "relevance", Limit: 20})
	if err != nil {
		return DiscoverySearchResult{}, err
	}
	return DiscoverySearchResult{Query: query, Page: page}, nil
}

func (s *DiscoveryService) ListQueries(ctx context.Context, projectID string) ([]Query, error) {
	projectID = strings.TrimSpace(projectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	return s.repository.ListQueries(ctx, projectID, 50)
}

func (s *DiscoveryService) ListCandidates(ctx context.Context, command CandidateListCommand) (CandidatePage, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return CandidatePage{}, err
	}
	values, err := s.repository.ListCandidates(ctx, projectID, strings.TrimSpace(command.QueryID))
	if err != nil {
		return CandidatePage{}, err
	}
	search := normalizeTitle(command.Search)
	filtered := make([]Candidate, 0, len(values))
	for _, value := range values {
		if command.Status != "" && value.ReviewStatus != command.Status {
			continue
		}
		if search != "" && !strings.Contains(normalizeTitle(value.Preferred.Title+" "+authorNames(value.Preferred.Authors)+" "+value.Preferred.Venue), search) {
			continue
		}
		filtered = append(filtered, value)
	}
	sortCandidates(filtered, command.Sort)
	offset, limit := command.Offset, command.Limit
	if offset < 0 {
		return CandidatePage{}, fmt.Errorf("candidate offset cannot be negative")
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return CandidatePage{}, fmt.Errorf("candidate page limit must be between 1 and 100")
	}
	page := CandidatePage{Items: []Candidate{}, Total: len(filtered), Offset: offset, Limit: limit}
	if offset >= len(filtered) {
		return page, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page.Items = filtered[offset:end]
	return page, nil
}

func (s *DiscoveryService) GetCandidate(ctx context.Context, projectID, candidateID string) (Candidate, error) {
	projectID, candidateID = strings.TrimSpace(projectID), strings.TrimSpace(candidateID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return Candidate{}, err
	}
	if candidateID == "" {
		return Candidate{}, fmt.Errorf("research candidate id is required")
	}
	return s.repository.GetCandidate(ctx, projectID, candidateID)
}

func (s *DiscoveryService) UpdateReview(ctx context.Context, command ReviewCommand) (Candidate, error) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.CandidateID = strings.TrimSpace(command.CandidateID)
	command.ExclusionReason = strings.TrimSpace(command.ExclusionReason)
	command.Note = strings.TrimSpace(command.Note)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return Candidate{}, err
	}
	if command.Status != ReviewPending && command.Status != ReviewIncluded && command.Status != ReviewExcluded {
		return Candidate{}, fmt.Errorf("candidate review status is invalid")
	}
	if command.Status == ReviewExcluded && command.ExclusionReason == "" {
		return Candidate{}, fmt.Errorf("excluded candidates require a reason")
	}
	if command.Status != ReviewExcluded {
		command.ExclusionReason = ""
	}
	if utf8.RuneCountInString(command.ExclusionReason) > 2000 || utf8.RuneCountInString(command.Note) > 20000 {
		return Candidate{}, fmt.Errorf("candidate review text is too long")
	}
	return s.repository.UpdateReview(ctx, command, s.now())
}

func (s *DiscoveryService) ImportCandidate(ctx context.Context, command ImportCandidateCommand) (result ImportCandidateResult, returnErr error) {
	projectID, candidateID := strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID)
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return result, err
	}
	if s.materializer == nil || s.attachments == nil || s.knowledge == nil {
		return result, fmt.Errorf("research import pipeline is not configured")
	}
	mode := command.Mode
	if mode == "" {
		mode = MaterializeAuto
	}
	if mode != MaterializeAuto && mode != MaterializeFullText && mode != MaterializeMetadata {
		return result, fmt.Errorf("research import mode is invalid")
	}
	candidate, err := s.repository.GetCandidate(ctx, projectID, candidateID)
	if err != nil {
		return result, err
	}
	if candidate.ReviewStatus != ReviewIncluded {
		return result, fmt.Errorf("include the candidate before importing it into the knowledge base")
	}
	if candidate.ImportStatus == ImportImported && candidate.AttachmentID != "" {
		return ImportCandidateResult{Candidate: candidate}, nil
	}
	now := s.now()
	if _, err := s.repository.UpdateImportState(ctx, ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, Status: ImportImporting, At: now}); err != nil {
		return result, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		message := boundedText(returnErr.Error(), 4000)
		_, _ = s.repository.UpdateImportState(context.WithoutCancel(ctx), ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, Status: ImportFailed, ErrorMessage: message, At: s.now()})
	}()
	materialized, err := s.materializer.Materialize(ctx, selected, candidate, mode)
	if err != nil {
		return result, err
	}
	defer s.materializer.Cleanup(materialized)
	imported, err := s.attachments.ImportResearchStaged(ctx, projectID, materialized.Path, materialized.Name)
	if err != nil {
		return result, err
	}
	if materialized.SHA256 != "" && !strings.EqualFold(materialized.SHA256, imported.SHA256) {
		return result, fmt.Errorf("research attachment SHA256 changed between download and import")
	}
	if err := s.knowledge.Enqueue(ctx, imported); err != nil {
		return result, err
	}
	candidate, err = s.repository.UpdateImportState(ctx, ImportStateCommand{ProjectID: projectID, CandidateID: candidateID, Status: ImportImported, Kind: materialized.Kind, AttachmentID: imported.ID, At: s.now()})
	if err != nil {
		return result, err
	}
	return ImportCandidateResult{Candidate: candidate, Attachment: imported}, nil
}

func SearchQueryKey(query string, sourceIDs []string, limit int) string {
	ids := append([]string(nil), sourceIDs...)
	for index := range ids {
		ids[index] = strings.ToLower(strings.TrimSpace(ids[index]))
	}
	sort.Strings(ids)
	payload := strings.ToLower(strings.Join(strings.Fields(query), " ")) + "\n" + strings.Join(ids, ",") + fmt.Sprintf("\n%d", limit)
	return hashText(payload)
}

func CandidateAliases(value Work) []CandidateAlias {
	aliases := make([]CandidateAlias, 0, 5)
	appendAlias := func(kind, raw string) {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			aliases = append(aliases, CandidateAlias{Kind: kind, Value: raw})
		}
	}
	appendAlias("doi", NormalizeDOI(value.Identifiers.DOI))
	appendAlias("pmid", digitsOnly(value.Identifiers.PMID))
	appendAlias("arxiv", strings.ToLower(NormalizeArXiv(value.Identifiers.ArXiv)))
	appendAlias("openalex", strings.ToUpper(normalizePrefixedID(value.Identifiers.OpenAlex, "https://openalex.org/")))
	title := normalizeTitle(value.Title)
	if value.Year >= 1000 && utf8.RuneCountInString(title) >= 16 {
		appendAlias("title_year", fmt.Sprintf("%d\t%s", value.Year, title))
	}
	return aliases
}

func CandidateKey(value Work) string {
	aliases := CandidateAliases(value)
	if len(aliases) > 0 {
		return hashText(aliases[0].Kind + ":" + aliases[0].Value)
	}
	return hashText("source:" + strings.ToLower(strings.TrimSpace(value.SourceID)) + ":" + strings.ToLower(strings.TrimSpace(value.SourceRecordID)))
}

func PreferredWork(records []SourceRecord) Work {
	if len(records) == 0 {
		return Work{Authors: []Author{}}
	}
	best := records[0].Work
	bestScore := workCompleteness(best)
	for _, record := range records[1:] {
		score := workCompleteness(record.Work)
		if score > bestScore || (score == bestScore && record.Work.SourceID < best.SourceID) {
			best, bestScore = record.Work, score
		}
	}
	best.RawSnapshot = nil
	if best.Authors == nil {
		best.Authors = []Author{}
	}
	return best
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func normalizeTitle(value string) string {
	var builder strings.Builder
	space := false
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			space = false
		} else if !space && builder.Len() > 0 {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func authorNames(values []Author) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	return strings.Join(names, " ")
}

func workCompleteness(value Work) int {
	score := 0
	if value.Abstract != "" {
		score += 8
	}
	if len(value.Authors) > 0 {
		score += 5
	}
	if value.Year > 0 {
		score += 2
	}
	if value.Venue != "" {
		score += 3
	}
	if value.Volume != "" || value.Issue != "" || value.Pages != "" {
		score += 2
	}
	if value.Identifiers.DOI != "" {
		score += 4
	}
	if value.Identifiers.PMID != "" || value.Identifiers.ArXiv != "" {
		score += 3
	}
	if value.PDFURL != "" {
		score += 2
	}
	return score
}

func sortCandidates(values []Candidate, mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	sort.SliceStable(values, func(i, j int) bool {
		a, b := values[i].Preferred, values[j].Preferred
		switch mode {
		case "year_desc":
			if a.Year != b.Year {
				return a.Year > b.Year
			}
		case "cited_desc":
			if a.CitedByCount != b.CitedByCount {
				return a.CitedByCount > b.CitedByCount
			}
		case "title":
			if normalizeTitle(a.Title) != normalizeTitle(b.Title) {
				return normalizeTitle(a.Title) < normalizeTitle(b.Title)
			}
		case "updated":
			if !values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
				return values[i].UpdatedAt.After(values[j].UpdatedAt)
			}
		default:
			if a.Score != b.Score {
				return a.Score > b.Score
			}
		}
		return values[i].ID < values[j].ID
	})
}

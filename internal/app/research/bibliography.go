package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type EvidenceLevel string

const (
	EvidenceNone             EvidenceLevel = "none"
	EvidenceFullText         EvidenceLevel = "full_text"
	EvidenceMetadataAbstract EvidenceLevel = "metadata_abstract"
)

type BibliographicAuthor struct {
	Name  string `json:"name"`
	ORCID string `json:"orcid,omitempty"`
}

// BibliographyData contains only explicitly observed or user-supplied fields.
// Empty values remain empty; the application never guesses missing metadata.
type BibliographyData struct {
	Authors        []BibliographicAuthor `json:"authors"`
	Year           int                   `json:"year,omitempty"`
	Title          string                `json:"title,omitempty"`
	Published      string                `json:"published,omitempty"`
	ContainerTitle string                `json:"containerTitle,omitempty"`
	Volume         string                `json:"volume,omitempty"`
	Issue          string                `json:"issue,omitempty"`
	Pages          string                `json:"pages,omitempty"`
	Publisher      string                `json:"publisher,omitempty"`
	DOI            string                `json:"doi,omitempty"`
	PMID           string                `json:"pmid,omitempty"`
	PMCID          string                `json:"pmcid,omitempty"`
	ArXiv          string                `json:"arxiv,omitempty"`
	OpenAlex       string                `json:"openAlex,omitempty"`
	URL            string                `json:"url,omitempty"`
	WorkType       string                `json:"workType,omitempty"`
	Language       string                `json:"language,omitempty"`
}

type BibliographySnapshot struct {
	SchemaVersion  int              `json:"schemaVersion"`
	BibliographyID string           `json:"bibliographyId"`
	Revision       int              `json:"revision"`
	Data           BibliographyData `json:"data"`
	CapturedAt     time.Time        `json:"capturedAt"`
}

type CitationSnapshot struct {
	BibliographyID string          `json:"bibliographyId"`
	Bibliography   json.RawMessage `json:"bibliography"`
	EvidenceLevel  EvidenceLevel   `json:"evidenceLevel"`
}

type BibliographyFieldSource struct {
	ID                     string          `json:"id"`
	Field                  string          `json:"field"`
	SourceRecordID         string          `json:"sourceRecordId,omitempty"`
	SourceIDSnapshot       string          `json:"sourceIdSnapshot"`
	SourceRecordIDSnapshot string          `json:"sourceRecordIdSnapshot"`
	Value                  json.RawMessage `json:"value"`
	ValueSHA256            string          `json:"valueSha256"`
	ObservedAt             time.Time       `json:"observedAt"`
}

type BibliographyRevision struct {
	ID                     string          `json:"id"`
	Revision               int             `json:"revision"`
	Field                  string          `json:"field"`
	Previous               json.RawMessage `json:"previous"`
	Next                   json.RawMessage `json:"next"`
	SourceKind             string          `json:"sourceKind"`
	SourceRecordIDSnapshot string          `json:"sourceRecordIdSnapshot,omitempty"`
	Reason                 string          `json:"reason,omitempty"`
	CreatedAt              time.Time       `json:"createdAt"`
}

type BibliographyMaterial struct {
	ID                          string        `json:"id"`
	AttachmentID                string        `json:"attachmentId,omitempty"`
	KnowledgeDocumentID         string        `json:"knowledgeDocumentId,omitempty"`
	AttachmentIDSnapshot        string        `json:"attachmentIdSnapshot"`
	KnowledgeDocumentIDSnapshot string        `json:"knowledgeDocumentIdSnapshot"`
	AttachmentSHA256Snapshot    string        `json:"attachmentSha256Snapshot"`
	ImportKind                  ImportKind    `json:"importKind"`
	EvidenceLevel               EvidenceLevel `json:"evidenceLevel"`
	CreatedAt                   time.Time     `json:"createdAt"`
	UpdatedAt                   time.Time     `json:"updatedAt"`
}

type Bibliography struct {
	ID              string                    `json:"id"`
	ProjectID       string                    `json:"projectId"`
	CandidateID     string                    `json:"candidateId"`
	Revision        int                       `json:"revision"`
	Data            BibliographyData          `json:"data"`
	SelectedSources map[string]string         `json:"selectedSources"`
	FieldSources    []BibliographyFieldSource `json:"fieldSources"`
	Revisions       []BibliographyRevision    `json:"revisions"`
	Materials       []BibliographyMaterial    `json:"materials"`
	CreatedAt       time.Time                 `json:"createdAt"`
	UpdatedAt       time.Time                 `json:"updatedAt"`
}

type EvidenceField string

const (
	EvidenceResearchQuestion EvidenceField = "research_question"
	EvidenceMethod           EvidenceField = "method"
	EvidenceSampleDataset    EvidenceField = "sample_dataset"
	EvidenceFinding          EvidenceField = "finding"
	EvidenceLimitation       EvidenceField = "limitation"
	EvidenceNote             EvidenceField = "note"
)

type EvidenceProvenance string

const (
	EvidenceUser  EvidenceProvenance = "user"
	EvidenceModel EvidenceProvenance = "model"
)

type EvidenceReviewStatus string

const (
	EvidencePending  EvidenceReviewStatus = "pending"
	EvidenceVerified EvidenceReviewStatus = "verified"
	EvidenceRejected EvidenceReviewStatus = "rejected"
)

type EvidenceReference struct {
	IndexVersionID string `json:"indexVersionId"`
	DocumentID     string `json:"documentId"`
	AttachmentID   string `json:"attachmentId"`
	ChunkID        string `json:"chunkId"`
}

type EvidenceSearchMatch struct {
	Reference  EvidenceReference `json:"reference"`
	SourceName string            `json:"sourceName"`
	Locator    string            `json:"locator"`
	Title      string            `json:"title,omitempty"`
	Snippet    string            `json:"snippet"`
	Rank       int               `json:"rank"`
}

type EvidenceSnapshot struct {
	IndexVersionID string `json:"indexVersionId"`
	DocumentID     string `json:"documentId"`
	AttachmentID   string `json:"attachmentId"`
	ChunkID        string `json:"chunkId"`
	SourceName     string `json:"sourceName"`
	Locator        string `json:"locator"`
	Quote          string `json:"quote"`
	QuoteSHA256    string `json:"quoteSha256"`
	SourceStart    int    `json:"sourceStart"`
	SourceEnd      int    `json:"sourceEnd"`
}

type EvidenceEntry struct {
	ID             string               `json:"id"`
	ProjectID      string               `json:"projectId"`
	BibliographyID string               `json:"bibliographyId"`
	Field          EvidenceField        `json:"field"`
	Content        string               `json:"content"`
	Provenance     EvidenceProvenance   `json:"provenance"`
	ReviewStatus   EvidenceReviewStatus `json:"reviewStatus"`
	EvidenceLevel  EvidenceLevel        `json:"evidenceLevel"`
	Evidence       *EvidenceSnapshot    `json:"evidence,omitempty"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      time.Time            `json:"updatedAt"`
}

type ReviseBibliographyCommand struct {
	ProjectID   string           `json:"projectId"`
	CandidateID string           `json:"candidateId"`
	Data        BibliographyData `json:"data"`
	Reason      string           `json:"reason,omitempty"`
}

type SelectBibliographySourceCommand struct {
	ProjectID      string `json:"projectId"`
	CandidateID    string `json:"candidateId"`
	Field          string `json:"field"`
	SourceRecordID string `json:"sourceRecordId"`
	Reason         string `json:"reason,omitempty"`
}

type SaveEvidenceCommand struct {
	ProjectID    string               `json:"projectId"`
	CandidateID  string               `json:"candidateId"`
	Field        EvidenceField        `json:"field"`
	Content      string               `json:"content"`
	Provenance   EvidenceProvenance   `json:"provenance"`
	ReviewStatus EvidenceReviewStatus `json:"reviewStatus"`
	Reference    *EvidenceReference   `json:"reference,omitempty"`
}

type ReviewEvidenceCommand struct {
	ProjectID    string               `json:"projectId"`
	CandidateID  string               `json:"candidateId"`
	EvidenceID   string               `json:"evidenceId"`
	ReviewStatus EvidenceReviewStatus `json:"reviewStatus"`
}

type BibliographyRepository interface {
	GetBibliography(ctx context.Context, projectID, candidateID string) (Bibliography, error)
	ReviseBibliography(ctx context.Context, command ReviseBibliographyCommand, at time.Time) (Bibliography, error)
	SelectBibliographySource(ctx context.Context, command SelectBibliographySourceCommand, at time.Time) (Bibliography, error)
	ListEvidence(ctx context.Context, projectID, candidateID string) ([]EvidenceEntry, error)
	SaveEvidence(ctx context.Context, command SaveEvidenceCommand, snapshot *EvidenceSnapshot, level EvidenceLevel, at time.Time) (EvidenceEntry, error)
	ReviewEvidence(ctx context.Context, command ReviewEvidenceCommand, at time.Time) (EvidenceEntry, error)
	DeleteEvidence(ctx context.Context, projectID, candidateID, evidenceID string) error
	CitationSnapshotForAttachment(ctx context.Context, projectID, attachmentID string, at time.Time) (CitationSnapshot, error)
}

type KnowledgeEvidenceVerifier interface {
	EvidenceChunk(ctx context.Context, projectID string, reference EvidenceReference) (EvidenceSnapshot, error)
	Search(ctx context.Context, projectID, query string, documentIDs []string) ([]EvidenceSearchMatch, error)
}

func (s *BibliographyService) SearchEvidence(ctx context.Context, projectID, candidateID, query string) ([]EvidenceSearchMatch, error) {
	projectID, candidateID, query = strings.TrimSpace(projectID), strings.TrimSpace(candidateID), strings.TrimSpace(query)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	if query == "" || utf8.RuneCountInString(query) > 200 {
		return nil, fmt.Errorf("evidence search query must contain between 1 and 200 characters")
	}
	bibliography, err := s.repository.GetBibliography(ctx, projectID, candidateID)
	if err != nil {
		return nil, err
	}
	documentIDs := []string{}
	for _, material := range bibliography.Materials {
		if material.KnowledgeDocumentID != "" {
			documentIDs = append(documentIDs, material.KnowledgeDocumentID)
		}
	}
	if len(documentIDs) == 0 {
		return nil, fmt.Errorf("this bibliography has no active local knowledge document")
	}
	return s.knowledge.Search(ctx, projectID, query, documentIDs)
}

type BibliographyService struct {
	repository BibliographyRepository
	projects   DiscoveryProjectLoader
	knowledge  KnowledgeEvidenceVerifier
	now        func() time.Time
}

func NewBibliographyService(repository BibliographyRepository, projects DiscoveryProjectLoader, knowledge KnowledgeEvidenceVerifier) (*BibliographyService, error) {
	if repository == nil || projects == nil || knowledge == nil {
		return nil, fmt.Errorf("bibliography dependencies are required")
	}
	return &BibliographyService{repository: repository, projects: projects, knowledge: knowledge, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *BibliographyService) Get(ctx context.Context, projectID, candidateID string) (Bibliography, error) {
	projectID, candidateID = strings.TrimSpace(projectID), strings.TrimSpace(candidateID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return Bibliography{}, err
	}
	return s.repository.GetBibliography(ctx, projectID, candidateID)
}

func (s *BibliographyService) CitationSnapshotForAttachment(ctx context.Context, projectID, attachmentID string) (CitationSnapshot, error) {
	projectID, attachmentID = strings.TrimSpace(projectID), strings.TrimSpace(attachmentID)
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return CitationSnapshot{}, err
	}
	if attachmentID == "" {
		return CitationSnapshot{}, fmt.Errorf("citation attachment id is required")
	}
	return s.repository.CitationSnapshotForAttachment(ctx, projectID, attachmentID, s.now())
}

func (s *BibliographyService) Revise(ctx context.Context, command ReviseBibliographyCommand) (Bibliography, error) {
	command.ProjectID, command.CandidateID, command.Reason = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID), strings.TrimSpace(command.Reason)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return Bibliography{}, err
	}
	command.Data = NormalizeBibliography(command.Data)
	if err := ValidateBibliography(command.Data); err != nil {
		return Bibliography{}, err
	}
	if utf8.RuneCountInString(command.Reason) > 2000 {
		return Bibliography{}, fmt.Errorf("bibliography revision reason is too long")
	}
	return s.repository.ReviseBibliography(ctx, command, s.now())
}

func (s *BibliographyService) SelectSource(ctx context.Context, command SelectBibliographySourceCommand) (Bibliography, error) {
	command.ProjectID, command.CandidateID = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID)
	command.Field, command.SourceRecordID, command.Reason = strings.TrimSpace(command.Field), strings.TrimSpace(command.SourceRecordID), strings.TrimSpace(command.Reason)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return Bibliography{}, err
	}
	if !ValidBibliographyField(command.Field) || command.SourceRecordID == "" || utf8.RuneCountInString(command.Reason) > 2000 {
		return Bibliography{}, fmt.Errorf("bibliography source selection is invalid")
	}
	return s.repository.SelectBibliographySource(ctx, command, s.now())
}

func (s *BibliographyService) ListEvidence(ctx context.Context, projectID, candidateID string) ([]EvidenceEntry, error) {
	if _, err := s.projects.Get(ctx, strings.TrimSpace(projectID)); err != nil {
		return nil, err
	}
	return s.repository.ListEvidence(ctx, strings.TrimSpace(projectID), strings.TrimSpace(candidateID))
}

func (s *BibliographyService) SaveEvidence(ctx context.Context, command SaveEvidenceCommand) (EvidenceEntry, error) {
	command.ProjectID, command.CandidateID = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID)
	command.Content = strings.TrimSpace(command.Content)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return EvidenceEntry{}, err
	}
	if !validEvidenceField(command.Field) || (command.Provenance != EvidenceUser && command.Provenance != EvidenceModel) || (command.ReviewStatus != EvidencePending && command.ReviewStatus != EvidenceVerified && command.ReviewStatus != EvidenceRejected) || command.Content == "" || utf8.RuneCountInString(command.Content) > 20000 {
		return EvidenceEntry{}, fmt.Errorf("evidence matrix entry is invalid")
	}
	if command.Provenance == EvidenceModel && command.ReviewStatus != EvidencePending {
		return EvidenceEntry{}, fmt.Errorf("model-derived evidence must be saved as pending before user verification")
	}
	var snapshot *EvidenceSnapshot
	level := EvidenceNone
	if command.Reference != nil {
		value, err := s.knowledge.EvidenceChunk(ctx, command.ProjectID, *command.Reference)
		if err != nil {
			return EvidenceEntry{}, err
		}
		snapshot = &value
		bibliography, err := s.repository.GetBibliography(ctx, command.ProjectID, command.CandidateID)
		if err != nil {
			return EvidenceEntry{}, err
		}
		for _, material := range bibliography.Materials {
			if material.AttachmentID == value.AttachmentID || material.AttachmentIDSnapshot == value.AttachmentID {
				level = material.EvidenceLevel
				break
			}
		}
		if level == EvidenceNone {
			return EvidenceEntry{}, fmt.Errorf("evidence chunk is not bound to this bibliography")
		}
	} else if command.Provenance == EvidenceModel || command.Field != EvidenceNote {
		return EvidenceEntry{}, fmt.Errorf("research facts require a local evidence chunk; only user notes may omit evidence")
	}
	return s.repository.SaveEvidence(ctx, command, snapshot, level, s.now())
}

func (s *BibliographyService) ReviewEvidence(ctx context.Context, command ReviewEvidenceCommand) (EvidenceEntry, error) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.CandidateID = strings.TrimSpace(command.CandidateID)
	command.EvidenceID = strings.TrimSpace(command.EvidenceID)
	if _, err := s.projects.Get(ctx, command.ProjectID); err != nil {
		return EvidenceEntry{}, err
	}
	if command.CandidateID == "" || command.EvidenceID == "" ||
		(command.ReviewStatus != EvidencePending && command.ReviewStatus != EvidenceVerified && command.ReviewStatus != EvidenceRejected) {
		return EvidenceEntry{}, fmt.Errorf("evidence review command is invalid")
	}
	return s.repository.ReviewEvidence(ctx, command, s.now())
}

func (s *BibliographyService) DeleteEvidence(ctx context.Context, projectID, candidateID, evidenceID string) error {
	if _, err := s.projects.Get(ctx, strings.TrimSpace(projectID)); err != nil {
		return err
	}
	return s.repository.DeleteEvidence(ctx, strings.TrimSpace(projectID), strings.TrimSpace(candidateID), strings.TrimSpace(evidenceID))
}

func BibliographyFromWork(value Work) BibliographyData {
	authors := make([]BibliographicAuthor, 0, len(value.Authors))
	for _, author := range value.Authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			authors = append(authors, BibliographicAuthor{Name: name, ORCID: strings.TrimSpace(author.ORCID)})
		}
	}
	return NormalizeBibliography(BibliographyData{
		Authors: authors, Year: value.Year, Title: value.Title, Published: value.Published, ContainerTitle: value.Venue,
		Volume: value.Volume, Issue: value.Issue, Pages: value.Pages, Publisher: value.Publisher,
		DOI: value.Identifiers.DOI, PMID: value.Identifiers.PMID, PMCID: value.Identifiers.PMCID,
		ArXiv: value.Identifiers.ArXiv, OpenAlex: value.Identifiers.OpenAlex, URL: value.LandingURL,
		WorkType: value.WorkType, Language: value.Language,
	})
}

func NormalizeBibliography(value BibliographyData) BibliographyData {
	value.Title, value.Published, value.ContainerTitle = cleanField(value.Title), cleanField(value.Published), cleanField(value.ContainerTitle)
	value.Volume, value.Issue, value.Pages, value.Publisher = cleanField(value.Volume), cleanField(value.Issue), cleanField(value.Pages), cleanField(value.Publisher)
	value.DOI, value.PMID, value.PMCID = NormalizeDOI(value.DOI), digitsOnly(value.PMID), strings.ToUpper(cleanField(value.PMCID))
	value.ArXiv = strings.ToLower(NormalizeArXiv(value.ArXiv))
	value.OpenAlex = strings.ToUpper(normalizePrefixedID(value.OpenAlex, "https://openalex.org/"))
	value.URL, value.WorkType, value.Language = cleanField(value.URL), cleanField(value.WorkType), strings.ToLower(cleanField(value.Language))
	seen := map[string]struct{}{}
	authors := make([]BibliographicAuthor, 0, len(value.Authors))
	for _, author := range value.Authors {
		author.Name, author.ORCID = cleanField(author.Name), cleanField(author.ORCID)
		if author.Name == "" {
			continue
		}
		key := strings.ToLower(author.Name + "\x00" + author.ORCID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		authors = append(authors, author)
	}
	value.Authors = authors
	return value
}

func ValidateBibliography(value BibliographyData) error {
	if value.Year < 0 || value.Year > 9999 || len(value.Authors) > 1000 {
		return fmt.Errorf("bibliography year or author count is invalid")
	}
	fields := []string{value.Title, value.Published, value.ContainerTitle, value.Volume, value.Issue, value.Pages, value.Publisher, value.DOI, value.PMID, value.PMCID, value.ArXiv, value.OpenAlex, value.URL, value.WorkType, value.Language}
	for _, field := range fields {
		if utf8.RuneCountInString(field) > 4096 {
			return fmt.Errorf("bibliography field is too long")
		}
	}
	for _, author := range value.Authors {
		if author.Name == "" || utf8.RuneCountInString(author.Name) > 500 || utf8.RuneCountInString(author.ORCID) > 128 {
			return fmt.Errorf("bibliography author is invalid")
		}
	}
	if value.URL != "" {
		parsed, err := url.Parse(value.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			return fmt.Errorf("bibliography URL is invalid")
		}
	}
	return nil
}

var bibliographyFields = []string{"authors", "year", "title", "published", "container_title", "volume", "issue", "pages", "publisher", "doi", "pmid", "pmcid", "arxiv", "openalex", "url", "work_type", "language"}

func BibliographyFields() []string { return append([]string(nil), bibliographyFields...) }

func ValidBibliographyField(value string) bool {
	index := sort.SearchStrings(bibliographyFieldsSorted, value)
	return index < len(bibliographyFieldsSorted) && bibliographyFieldsSorted[index] == value
}

var bibliographyFieldsSorted = func() []string {
	values := append([]string(nil), bibliographyFields...)
	sort.Strings(values)
	return values
}()

func BibliographyFieldValue(value BibliographyData, field string) (json.RawMessage, bool) {
	var selected any
	switch field {
	case "authors":
		selected = value.Authors
	case "year":
		selected = value.Year
	case "title":
		selected = value.Title
	case "published":
		selected = value.Published
	case "container_title":
		selected = value.ContainerTitle
	case "volume":
		selected = value.Volume
	case "issue":
		selected = value.Issue
	case "pages":
		selected = value.Pages
	case "publisher":
		selected = value.Publisher
	case "doi":
		selected = value.DOI
	case "pmid":
		selected = value.PMID
	case "pmcid":
		selected = value.PMCID
	case "arxiv":
		selected = value.ArXiv
	case "openalex":
		selected = value.OpenAlex
	case "url":
		selected = value.URL
	case "work_type":
		selected = value.WorkType
	case "language":
		selected = value.Language
	default:
		return nil, false
	}
	encoded, _ := json.Marshal(selected)
	nonempty := string(encoded) != `""` && string(encoded) != "0" && string(encoded) != "[]" && string(encoded) != "null"
	return encoded, nonempty
}

func SetBibliographyField(value *BibliographyData, field string, raw json.RawMessage) error {
	if value == nil || !ValidBibliographyField(field) || !json.Valid(raw) {
		return fmt.Errorf("bibliography field value is invalid")
	}
	var target any
	switch field {
	case "authors":
		target = &value.Authors
	case "year":
		target = &value.Year
	case "title":
		target = &value.Title
	case "published":
		target = &value.Published
	case "container_title":
		target = &value.ContainerTitle
	case "volume":
		target = &value.Volume
	case "issue":
		target = &value.Issue
	case "pages":
		target = &value.Pages
	case "publisher":
		target = &value.Publisher
	case "doi":
		target = &value.DOI
	case "pmid":
		target = &value.PMID
	case "pmcid":
		target = &value.PMCID
	case "arxiv":
		target = &value.ArXiv
	case "openalex":
		target = &value.OpenAlex
	case "url":
		target = &value.URL
	case "work_type":
		target = &value.WorkType
	case "language":
		target = &value.Language
	}
	return json.Unmarshal(raw, target)
}

func BibliographyValueSHA256(value json.RawMessage) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validEvidenceField(value EvidenceField) bool {
	switch value {
	case EvidenceResearchQuestion, EvidenceMethod, EvidenceSampleDataset, EvidenceFinding, EvidenceLimitation, EvidenceNote:
		return true
	default:
		return false
	}
}

func cleanField(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

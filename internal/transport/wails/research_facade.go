package wails

import (
	"github.com/wangh00/SciAide/internal/app/attachment"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type ResearchFacade struct {
	lifecycle    *LifecycleContext
	service      *appresearch.DiscoveryService
	bibliography *appresearch.BibliographyService
}

func NewResearchFacade(lifecycle *LifecycleContext, service *appresearch.DiscoveryService, bibliography *appresearch.BibliographyService) *ResearchFacade {
	return &ResearchFacade{lifecycle: lifecycle, service: service, bibliography: bibliography}
}

func (f *ResearchFacade) GetBibliography(projectID, candidateID string) (appresearch.Bibliography, error) {
	return f.bibliography.Get(f.lifecycle.Context(), projectID, candidateID)
}

func (f *ResearchFacade) GetBibliographyForTask(projectID, candidateID, taskID string) (appresearch.Bibliography, error) {
	return f.bibliography.GetForTask(f.lifecycle.Context(), projectID, candidateID, taskID)
}

func (f *ResearchFacade) ReviseBibliography(command appresearch.ReviseBibliographyCommand) (appresearch.Bibliography, error) {
	return f.bibliography.Revise(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) SelectBibliographySource(command appresearch.SelectBibliographySourceCommand) (appresearch.Bibliography, error) {
	return f.bibliography.SelectSource(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) SearchEvidence(projectID, candidateID, query string) ([]appresearch.EvidenceSearchMatch, error) {
	return f.bibliography.SearchEvidence(f.lifecycle.Context(), projectID, candidateID, query)
}

func (f *ResearchFacade) SearchEvidenceForTask(projectID, candidateID, taskID, query string) ([]appresearch.EvidenceSearchMatch, error) {
	return f.bibliography.SearchEvidenceForTask(f.lifecycle.Context(), projectID, candidateID, taskID, query)
}

func (f *ResearchFacade) ListEvidence(projectID, candidateID string) ([]appresearch.EvidenceEntry, error) {
	return f.bibliography.ListEvidence(f.lifecycle.Context(), projectID, candidateID)
}

func (f *ResearchFacade) ListEvidenceForTask(projectID, candidateID, taskID string) ([]appresearch.EvidenceEntry, error) {
	return f.bibliography.ListEvidenceForTask(f.lifecycle.Context(), projectID, candidateID, taskID)
}

func (f *ResearchFacade) SaveEvidence(command appresearch.SaveEvidenceCommand) (appresearch.EvidenceEntry, error) {
	return f.bibliography.SaveEvidence(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) ReviewEvidence(command appresearch.ReviewEvidenceCommand) (appresearch.EvidenceEntry, error) {
	return f.bibliography.ReviewEvidence(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) DeleteEvidence(projectID, candidateID, evidenceID string) error {
	return f.bibliography.DeleteEvidence(f.lifecycle.Context(), projectID, candidateID, evidenceID)
}

func (f *ResearchFacade) ReviewEvidenceForTask(command appresearch.ReviewEvidenceCommand) (appresearch.EvidenceEntry, error) {
	return f.bibliography.ReviewEvidence(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) DeleteEvidenceForTask(projectID, candidateID, taskID, evidenceID string) error {
	return f.bibliography.DeleteEvidenceForTask(f.lifecycle.Context(), projectID, candidateID, taskID, evidenceID)
}

func (f *ResearchFacade) Catalog() []appresearch.Source {
	return f.service.Catalog()
}

func (f *ResearchFacade) Search(command appresearch.DiscoverySearchCommand) (appresearch.DiscoverySearchResult, error) {
	return f.service.Search(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) ListQueries(projectID string) ([]appresearch.Query, error) {
	return f.service.ListQueries(f.lifecycle.Context(), projectID)
}
func (f *ResearchFacade) QueryHistory(c appresearch.QueryPageCommand) ([]appresearch.Query, error) {
	return f.service.QueryHistory(f.lifecycle.Context(), c)
}
func (f *ResearchFacade) QueryOrigins(projectID string) ([]appresearch.QueryOrigin, error) {
	return f.service.QueryOrigins(f.lifecycle.Context(), projectID)
}
func (f *ResearchFacade) DeleteQueryHistory(c appresearch.DeleteQueriesCommand) error {
	return f.service.DeleteQueryHistory(f.lifecycle.Context(), c)
}
func (f *ResearchFacade) DeleteQueryCandidates(c appresearch.DeleteCandidatesCommand) error {
	return f.service.DeleteQueryCandidates(f.lifecycle.Context(), c)
}

func (f *ResearchFacade) ListCandidates(command appresearch.CandidateListCommand) (appresearch.CandidatePage, error) {
	return f.service.ListCandidates(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) UpdateReview(command appresearch.ReviewCommand) (appresearch.Candidate, error) {
	return f.service.UpdateReview(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) ImportCandidate(command appresearch.ImportCandidateCommand) (appresearch.ImportCandidateResult, error) {
	return f.service.ImportCandidate(f.lifecycle.Context(), command)
}

func (f *ResearchFacade) CollectCandidate(projectID, candidateID, taskID string) (attachment.Material, error) {
	return f.service.CollectCandidate(f.lifecycle.Context(), projectID, candidateID, taskID)
}

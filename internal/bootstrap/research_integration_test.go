package bootstrap

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/chat"
	appcitation "github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestResearchCandidateMetadataImportReachesKnowledgeIndexIdempotently(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	application, err := New(Options{RootDir: sourceRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	created, err := application.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "Research import"})
	if err != nil {
		t.Fatal(err)
	}
	repository := sqlite.NewResearchRepository(application.store.DB())
	command := appresearch.SearchCommand{Query: "replication evidence", SourceIDs: []string{"crossref"}, Limit: 5}
	search := appresearch.SearchResult{
		Query: command.Query,
		Works: []appresearch.Work{{
			SourceID: "crossref", SourceRecordID: "10.5555/replication", Title: "Replication Evidence",
			Abstract: "The fixture finding is epsilon forty two.", Authors: []appresearch.Author{{Name: "Ada Researcher", ORCID: "0000-0001-2345-6789"}}, Year: 2026,
			Identifiers: appresearch.Identifiers{DOI: "10.5555/replication"}, LandingURL: "https://doi.org/10.5555/replication",
			RawSnapshot: json.RawMessage(`{"DOI":"10.5555/replication"}`),
		}},
		Sources: []appresearch.SourceSearch{{SourceID: "crossref", Status: appresearch.SearchOK, Count: 1}},
	}
	query, err := repository.SaveSearch(ctx, created.ID, appresearch.SearchQueryKey(command.Query, command.SourceIDs, command.Limit), command, search, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	page, err := application.ResearchFacade.ListCandidates(appresearch.CandidateListCommand{ProjectID: created.ID, QueryID: query.ID, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("candidates = %#v, %v", page, err)
	}
	candidate, err := application.ResearchFacade.UpdateReview(appresearch.ReviewCommand{ProjectID: created.ID, CandidateID: page.Items[0].ID, Status: appresearch.ReviewIncluded, Note: "integration fixture"})
	if err != nil || candidate.ReviewStatus != appresearch.ReviewIncluded {
		t.Fatalf("review = %#v, %v", candidate, err)
	}
	imported, err := application.ResearchFacade.ImportCandidate(appresearch.ImportCandidateCommand{ProjectID: created.ID, CandidateID: candidate.ID, Mode: appresearch.MaterializeMetadata})
	if err != nil || imported.Candidate.ImportStatus != appresearch.ImportImported || imported.Candidate.ImportKind != appresearch.ImportMetadataAbstract || imported.Attachment.ID == "" {
		t.Fatalf("import = %#v, %v", imported, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		documents, listErr := application.KnowledgeFacade.ListDocuments(created.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(documents) == 1 && documents[0].Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("knowledge document did not become ready: %#v", documents)
		}
		time.Sleep(20 * time.Millisecond)
	}
	result, err := application.knowledge.Search(ctx, created.ID, "epsilon forty two", 5)
	if err != nil || len(result.Matches) == 0 || result.Matches[0].AttachmentID != imported.Attachment.ID {
		t.Fatalf("knowledge search = %#v, %v", result, err)
	}
	evidenceMatches, err := application.ResearchFacade.SearchEvidence(created.ID, candidate.ID, "epsilon forty two")
	if err != nil || len(evidenceMatches) == 0 || evidenceMatches[0].Reference.AttachmentID != imported.Attachment.ID {
		t.Fatalf("research evidence search = %#v, %v", evidenceMatches, err)
	}
	if _, err := application.ResearchFacade.SaveEvidence(appresearch.SaveEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, Field: appresearch.EvidenceFinding,
		Content: "Model claim", Provenance: appresearch.EvidenceModel, ReviewStatus: appresearch.EvidenceVerified,
		Reference: &evidenceMatches[0].Reference,
	}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("verified model evidence error = %v", err)
	}
	if _, err := application.ResearchFacade.SaveEvidence(appresearch.SaveEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, Field: appresearch.EvidenceFinding,
		Content: "Model claim", Provenance: appresearch.EvidenceModel, ReviewStatus: appresearch.EvidenceRejected,
		Reference: &evidenceMatches[0].Reference,
	}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("pre-rejected model evidence error = %v", err)
	}
	savedEvidence, err := application.ResearchFacade.SaveEvidence(appresearch.SaveEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, Field: appresearch.EvidenceFinding,
		Content: "The fixture finding is epsilon forty two.", Provenance: appresearch.EvidenceModel, ReviewStatus: appresearch.EvidencePending,
		Reference: &evidenceMatches[0].Reference,
	})
	if err != nil || savedEvidence.Evidence == nil || savedEvidence.EvidenceLevel != appresearch.EvidenceMetadataAbstract || savedEvidence.ReviewStatus != appresearch.EvidencePending {
		t.Fatalf("saved metadata evidence = %#v, %v", savedEvidence, err)
	}
	if _, err := application.store.DB().ExecContext(ctx, `UPDATE research_evidence_entries SET content='tampered',provenance='user' WHERE id=?`, savedEvidence.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("evidence content mutation error = %v", err)
	}
	verifiedEvidence, err := application.ResearchFacade.ReviewEvidence(appresearch.ReviewEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, EvidenceID: savedEvidence.ID, ReviewStatus: appresearch.EvidenceVerified,
	})
	if err != nil || verifiedEvidence.ReviewStatus != appresearch.EvidenceVerified || verifiedEvidence.Content != savedEvidence.Content ||
		verifiedEvidence.Field != savedEvidence.Field || verifiedEvidence.Provenance != savedEvidence.Provenance || verifiedEvidence.EvidenceLevel != savedEvidence.EvidenceLevel ||
		!reflect.DeepEqual(verifiedEvidence.Evidence, savedEvidence.Evidence) {
		t.Fatalf("reviewed evidence changed immutable content: before=%#v after=%#v err=%v", savedEvidence, verifiedEvidence, err)
	}
	rejectedEvidence, err := application.ResearchFacade.ReviewEvidence(appresearch.ReviewEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, EvidenceID: savedEvidence.ID, ReviewStatus: appresearch.EvidenceRejected,
	})
	if err != nil || rejectedEvidence.ReviewStatus != appresearch.EvidenceRejected || !reflect.DeepEqual(rejectedEvidence.Evidence, savedEvidence.Evidence) {
		t.Fatalf("rejected evidence changed immutable snapshot: before=%#v after=%#v err=%v", savedEvidence, rejectedEvidence, err)
	}
	verifiedEvidence, err = application.ResearchFacade.ReviewEvidence(appresearch.ReviewEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, EvidenceID: savedEvidence.ID, ReviewStatus: appresearch.EvidenceVerified,
	})
	if err != nil || verifiedEvidence.ReviewStatus != appresearch.EvidenceVerified || !reflect.DeepEqual(verifiedEvidence.Evidence, savedEvidence.Evidence) {
		t.Fatalf("re-verified evidence changed immutable snapshot: before=%#v after=%#v err=%v", savedEvidence, verifiedEvidence, err)
	}
	if _, err := application.ResearchFacade.ReviewEvidence(appresearch.ReviewEvidenceCommand{
		ProjectID: created.ID, CandidateID: candidate.ID, EvidenceID: savedEvidence.ID, ReviewStatus: appresearch.EvidenceReviewStatus("invalid"),
	}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid evidence review error = %v", err)
	}
	savedEvidence = verifiedEvidence
	bibliography, err := application.ResearchFacade.GetBibliography(created.ID, candidate.ID)
	if err != nil || len(bibliography.Data.Authors) != 1 || bibliography.Data.Authors[0].ORCID != "0000-0001-2345-6789" || len(bibliography.Materials) != 1 || bibliography.Materials[0].EvidenceLevel != appresearch.EvidenceMetadataAbstract {
		t.Fatalf("research bibliography = %#v, %v", bibliography, err)
	}
	replayedImport, err := application.ResearchFacade.ImportCandidate(appresearch.ImportCandidateCommand{ProjectID: created.ID, CandidateID: candidate.ID})
	if err != nil || replayedImport.Candidate.AttachmentID != imported.Attachment.ID {
		t.Fatalf("idempotent research import = %#v, %v", replayedImport, err)
	}
	attachments, err := application.AttachmentFacade.ListProjectAttachments(created.ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("duplicate import attachments = %#v, %v", attachments, err)
	}

	conversationValue, err := application.ConversationFacade.CreateConversation(wailstransport.CreateConversationRequest{ProjectID: created.ID, Title: "Evidence report"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{
		ID: "research-citation-profile", Name: "Research citation fixture", ProviderType: modelprofile.ProviderOpenAICompatible,
		APIProtocol: modelcap.ProtocolOpenAIChat, BaseURL: "https://example.test/v1", ModelID: "fixture-model",
		Models: []modelprofile.ProfileModel{{ID: "fixture-model", Enabled: true, IsDefault: true}}, SecretRef: "fixture-secret",
		TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := sqlite.NewModelProfileRepository(application.store.DB()).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	run := chat.Run{
		ID: "research-citation-run", ConversationID: conversationValue.ID, UserMessageID: "research-citation-user",
		AssistantMessageID: "research-citation-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID,
		APIProtocol: profile.APIProtocol, PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning,
		CreatedAt: now, StartedAt: &now, UpdatedAt: now,
	}
	userMessage := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: "research-citation-user-part", MessageID: run.UserMessageID, Type: "text", Text: "What is the finding?", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	assistantMessage := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{ID: "research-citation-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	runRepository := sqlite.NewRunRepository(application.store.DB())
	if err := runRepository.CreateWithMessages(ctx, run, userMessage, assistantMessage); err != nil {
		t.Fatal(err)
	}
	definition, err := builtin.NewSearchKnowledge(application.knowledge).Definition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	toolService := tool.NewService(sqlite.NewToolRepository(application.store.DB()), tool.JSONSchemaValidator{})
	call, err := toolService.Propose(ctx, definition, tool.CreateCommand{RunID: run.ID, ProviderCallID: "research-knowledge-call", Arguments: json.RawMessage(`{"query":"epsilon forty two","limit":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if call, err = toolService.Start(ctx, call.ID); err != nil {
		t.Fatal(err)
	}
	execution, err := application.tools.Execute(ctx, created.ID, call.ID)
	if err != nil || execution.Result.Status != tool.ResultSuccess || len(execution.Result.Citations) == 0 {
		t.Fatalf("knowledge tool execution = %#v, %v", execution, err)
	}
	answer := "The finding is supported by local evidence " + execution.Result.Citations[0].Reference
	calls, err := toolService.ListByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	citations := appcitation.Resolve(run.ID, assistantMessage.ID, answer, calls, completedAt)
	if len(citations) != 1 {
		t.Fatalf("resolved citations = %#v", citations)
	}
	run.Status, run.FinishReason, run.CompletedAt, run.UpdatedAt = chat.RunCompleted, "stop", &completedAt, completedAt
	if err := runRepository.Complete(ctx, run, answer, citations); err != nil {
		t.Fatal(err)
	}
	messages, err := application.ConversationFacade.ListMessages(conversationValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	var messageCitation conversation.Citation
	for _, message := range messages {
		if message.ID == assistantMessage.ID && len(message.Citations) == 1 {
			messageCitation = message.Citations[0]
		}
	}
	var bibliographySnapshot appresearch.BibliographySnapshot
	if messageCitation.BibliographyID != bibliography.ID || messageCitation.EvidenceLevel != string(appresearch.EvidenceMetadataAbstract) || json.Unmarshal(messageCitation.Bibliography, &bibliographySnapshot) != nil || bibliographySnapshot.Data.DOI != "10.5555/replication" {
		t.Fatalf("message citation bibliography snapshot = %#v, raw=%s", messageCitation, messageCitation.Bibliography)
	}
	savedArtifact, err := application.ArtifactFacade.SaveAssistantAnswer(wailstransport.SaveAssistantArtifactRequest{ProjectID: created.ID, MessageID: assistantMessage.ID, Name: "Evidence report"})
	if err != nil || len(savedArtifact.Version.Citations) != 1 {
		t.Fatalf("save cited Artifact = %#v, %v", savedArtifact, err)
	}
	exportedArtifact, err := application.ArtifactFacade.CreateArtifactExport(artifact.ExportCommand{ProjectID: created.ID, VersionID: savedArtifact.Version.ID, Format: artifact.ExportPDF, CitationStyle: artifact.CitationGB7714})
	if err != nil || !exportedArtifact.Created || exportedArtifact.Export.SourceSHA256 != savedArtifact.Version.SHA256 {
		t.Fatalf("create cited Artifact export = %#v, %v", exportedArtifact, err)
	}
	verifyResearchArchiveRoundTrip(t, application, sourceRoot, created.ID, candidate, bibliography, savedEvidence, savedArtifact, exportedArtifact.Export)

	documents, err := application.KnowledgeFacade.ListDocuments(created.ID)
	if err != nil || len(documents) != 1 {
		t.Fatalf("knowledge documents = %#v, %v", documents, err)
	}
	if _, err := application.KnowledgeFacade.RemoveDocument(created.ID, documents[0].ID); err != nil {
		t.Fatal(err)
	}
	persistedEvidence, err := application.ResearchFacade.ListEvidence(created.ID, candidate.ID)
	if err != nil || len(persistedEvidence) != 1 || persistedEvidence[0].Evidence == nil || persistedEvidence[0].Evidence.QuoteSHA256 != savedEvidence.Evidence.QuoteSHA256 {
		t.Fatalf("evidence snapshot after knowledge removal = %#v, %v", persistedEvidence, err)
	}
	if _, err := application.store.DB().ExecContext(ctx, `DELETE FROM research_candidates WHERE id=? AND project_id=?`, candidate.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := application.ConversationFacade.RemoveConversation(conversationValue.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := application.ArtifactFacade.GetArtifact(created.ID, savedArtifact.Artifact.ID)
	if err != nil || len(detail.Versions) != 1 || len(detail.Versions[0].Citations) != 1 {
		t.Fatalf("Artifact after source deletion = %#v, %v", detail, err)
	}
	artifactCitation := detail.Versions[0].Citations[0]
	var artifactBibliography appresearch.BibliographySnapshot
	if artifactCitation.BibliographyIDSnapshot != bibliography.ID || artifactCitation.EvidenceLevel != string(appresearch.EvidenceMetadataAbstract) || json.Unmarshal(artifactCitation.BibliographySnapshot, &artifactBibliography) != nil || artifactBibliography.Data.DOI != bibliographySnapshot.Data.DOI || artifactCitation.SourceRunIDSnapshot != run.ID || artifactCitation.SourceMessageIDSnapshot != assistantMessage.ID {
		t.Fatalf("immutable Artifact citation snapshot = %#v, raw=%s", artifactCitation, artifactCitation.BibliographySnapshot)
	}
}

func verifyResearchArchiveRoundTrip(t *testing.T, source *Application, sourceRoot, projectID string, candidate appresearch.Candidate, bibliography appresearch.Bibliography, evidence appresearch.EvidenceEntry, saved artifact.SaveResult, exported artifact.Export) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	firstArchive := filepath.Join(root, "research-first.sciaide-project")
	firstService := projectArchiveServiceForTest(t, source, sourceRoot)
	first, err := firstService.Export(ctx, projectID, firstArchive)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Stats.Candidates != 1 || first.Manifest.Stats.Bibliographies != 1 || first.Manifest.Stats.Evidence != 1 || first.Manifest.Stats.Artifacts != 1 || first.Manifest.Stats.Documents != 1 {
		t.Fatalf("research archive stats = %#v", first.Manifest.Stats)
	}
	firstObjects := archivePayloadDigests(t, firstArchive)
	sourceChunks := knowledgeChunkSnapshot(t, source, projectID)

	restoredRoot := filepath.Join(root, "restored-home")
	restoredApp, err := New(Options{RootDir: restoredRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer restoredApp.Close()
	restoreService := projectArchiveServiceForTest(t, restoredApp, restoredRoot)
	restored, err := restoreService.Restore(ctx, projectarchive.RestoreCommand{Path: firstArchive, Name: "Restored research chain"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Project.ID == projectID || restored.SourceProjectID != projectID {
		t.Fatalf("restored project identity = %#v", restored)
	}
	queries, err := restoredApp.ResearchFacade.ListQueries(restored.Project.ID)
	if err != nil || len(queries) != 1 || queries[0].ID == "" || queries[0].ProjectID != restored.Project.ID {
		t.Fatalf("restored research queries = %#v, %v", queries, err)
	}
	page, err := restoredApp.ResearchFacade.ListCandidates(appresearch.CandidateListCommand{ProjectID: restored.Project.ID, QueryID: queries[0].ID, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("restored research candidates = %#v, %v", page, err)
	}
	restoredCandidate := page.Items[0]
	if restoredCandidate.ID == candidate.ID || restoredCandidate.AttachmentID == candidate.AttachmentID || restoredCandidate.Preferred.Identifiers.DOI != candidate.Preferred.Identifiers.DOI || restoredCandidate.ReviewStatus != candidate.ReviewStatus {
		t.Fatalf("restored candidate identity/content = %#v", restoredCandidate)
	}
	restoredBibliography, err := restoredApp.ResearchFacade.GetBibliography(restored.Project.ID, restoredCandidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredBibliography.ID == bibliography.ID || restoredBibliography.Revision != bibliography.Revision || restoredBibliography.Data.DOI != bibliography.Data.DOI || len(restoredBibliography.FieldSources) != len(bibliography.FieldSources) || len(restoredBibliography.Materials) != 1 {
		t.Fatalf("restored bibliography = %#v", restoredBibliography)
	}
	for field, selection := range restoredBibliography.SelectedSources {
		if selection == "user" {
			continue
		}
		if !strings.HasPrefix(selection, "auto:") && !strings.HasPrefix(selection, "source:") || strings.Contains(selection, candidate.Records[0].ID) {
			t.Fatalf("bibliography source selection %s=%q was not remapped", field, selection)
		}
	}
	restoredEvidence, err := restoredApp.ResearchFacade.ListEvidence(restored.Project.ID, restoredCandidate.ID)
	if err != nil || len(restoredEvidence) != 1 || restoredEvidence[0].ID == evidence.ID || restoredEvidence[0].Evidence == nil || evidence.Evidence == nil {
		t.Fatalf("restored evidence = %#v, %v", restoredEvidence, err)
	}
	if restoredEvidence[0].Evidence.QuoteSHA256 != evidence.Evidence.QuoteSHA256 || restoredEvidence[0].Evidence.Quote != evidence.Evidence.Quote || restoredEvidence[0].Evidence.ChunkID != evidence.Evidence.ChunkID || restoredEvidence[0].Evidence.AttachmentID != evidence.Evidence.AttachmentID || restoredEvidence[0].EvidenceLevel != evidence.EvidenceLevel {
		t.Fatalf("evidence snapshot changed during restore: before=%#v after=%#v", evidence, restoredEvidence[0])
	}
	search, err := restoredApp.knowledge.Search(ctx, restored.Project.ID, "epsilon forty two", 5)
	if err != nil || len(search.Matches) == 0 || search.Matches[0].AttachmentID != restoredCandidate.AttachmentID || search.Matches[0].AttachmentID == evidence.Evidence.AttachmentID || !strings.Contains(search.Matches[0].Snippet, "epsilon forty two") {
		t.Fatalf("restored knowledge search = %#v, %v", search, err)
	}
	restoredChunks := knowledgeChunkSnapshot(t, restoredApp, restored.Project.ID)
	if string(sourceChunks) != string(restoredChunks) {
		t.Fatalf("knowledge Chunk contents changed: before=%s after=%s", sourceChunks, restoredChunks)
	}
	restoredArtifacts, err := restoredApp.ArtifactFacade.ListArtifacts(restored.Project.ID, true)
	if err != nil || len(restoredArtifacts) != 1 || restoredArtifacts[0].ID == saved.Artifact.ID {
		t.Fatalf("restored Artifacts = %#v, %v", restoredArtifacts, err)
	}
	detail, err := restoredApp.ArtifactFacade.GetArtifact(restored.Project.ID, restoredArtifacts[0].ID)
	if err != nil || len(detail.Versions) != 1 || len(detail.Versions[0].Citations) != 1 || len(detail.Versions[0].Exports) != 1 {
		t.Fatalf("restored Artifact detail = %#v, %v", detail, err)
	}
	restoredVersion, restoredCitation, restoredExport := detail.Versions[0], detail.Versions[0].Citations[0], detail.Versions[0].Exports[0]
	originalCitation := saved.Version.Citations[0]
	if restoredVersion.ID == saved.Version.ID || restoredVersion.SHA256 != saved.Version.SHA256 || restoredExport.ID == exported.ID || restoredExport.SHA256 != exported.SHA256 || restoredExport.SourceSHA256 != exported.SourceSHA256 {
		t.Fatalf("restored Artifact bytes = version:%#v export:%#v", restoredVersion, restoredExport)
	}
	if restoredCitation.SourceRunIDSnapshot != originalCitation.SourceRunIDSnapshot || restoredCitation.SourceMessageIDSnapshot != originalCitation.SourceMessageIDSnapshot || restoredCitation.SourceToolCallIDSnapshot != originalCitation.SourceToolCallIDSnapshot || restoredCitation.QuoteSHA256 != originalCitation.QuoteSHA256 || restoredCitation.BibliographyIDSnapshot != originalCitation.BibliographyIDSnapshot || string(restoredCitation.BibliographySnapshot) != string(originalCitation.BibliographySnapshot) || restoredCitation.EvidenceLevel != originalCitation.EvidenceLevel {
		t.Fatalf("Artifact citation snapshot changed: before=%#v after=%#v", originalCitation, restoredCitation)
	}
	secondArchive := filepath.Join(root, "research-second.sciaide-project")
	second, err := restoreService.Export(ctx, restored.Project.ID, secondArchive)
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.Stats != first.Manifest.Stats {
		t.Fatalf("archive relation counts changed: first=%#v second=%#v", first.Manifest.Stats, second.Manifest.Stats)
	}
	secondObjects := archivePayloadDigests(t, secondArchive)
	for name, digest := range firstObjects {
		if name == "project.sqlite" || strings.HasPrefix(name, "files/cache/knowledge/") {
			continue
		}
		if secondObjects[name] != digest {
			t.Fatalf("archive payload %q changed: first=%s second=%s", name, digest, secondObjects[name])
		}
	}
}

func knowledgeChunkSnapshot(t *testing.T, application *Application, projectID string) []byte {
	t.Helper()
	var workspace, relative string
	if err := application.store.DB().QueryRow(`
		SELECT p.workspace_path,v.storage_relative_path
		FROM projects p JOIN knowledge_index_versions v ON v.project_id=p.id
		WHERE p.id=? AND v.status='ready'`, projectID).Scan(&workspace, &relative); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.OpenExisting(context.Background(), filepath.Join(workspace, project.PrivateDirectoryName, filepath.FromSlash(relative)), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,ordinal,unit_index,kind,locator,source_start,source_end,title,content,content_sha256 FROM chunks ORDER BY ordinal,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type stableChunk struct {
		ID, Kind, Locator, Title, Content, ContentSHA256 string
		Ordinal, UnitIndex, SourceStart, SourceEnd       int
	}
	values := []stableChunk{}
	for rows.Next() {
		var value stableChunk
		if err := rows.Scan(&value.ID, &value.Ordinal, &value.UnitIndex, &value.Kind, &value.Locator, &value.SourceStart, &value.SourceEnd, &value.Title, &value.Content, &value.ContentSHA256); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func projectArchiveServiceForTest(t *testing.T, application *Application, root string) *projectarchive.Service {
	t.Helper()
	projects := project.NewService(sqlite.NewProjectRepository(application.store.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	service, err := projectarchive.NewService(sqlite.NewProjectArchiveRepository(application.store.DB()), projects, filepath.Join(root, "data", "workspaces"), filepath.Join(root, "cache", "project-archives"), filepath.Join(root, "backups", "trash"), "0.4.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func archivePayloadDigests(t *testing.T, archivePath string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := map[string]string{}
	for _, entry := range reader.File {
		if entry.Name == "manifest.json" {
			continue
		}
		input, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, input); err != nil {
			input.Close()
			t.Fatal(err)
		}
		if err := input.Close(); err != nil {
			t.Fatal(err)
		}
		result[entry.Name] = hex.EncodeToString(hash.Sum(nil))
	}
	return result
}

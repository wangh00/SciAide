package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CurrentSchemaVersion = 1
	DefaultContextTokens = 8_000
	MaxContextTokens     = 32_000
)

var (
	ErrSkillNotFound        = errors.New("Skill not found")
	ErrProjectSkillNotFound = errors.New("project Skill not found")
)

type ActivationMode string

const (
	ActivationExplicit ActivationMode = "explicit"
	ActivationSuggest  ActivationMode = "suggest"
)

type IntegrityStatus string

const (
	IntegrityValid   IntegrityStatus = "valid"
	IntegrityInvalid IntegrityStatus = "invalid"
	IntegrityMissing IntegrityStatus = "missing"
)

type AvailabilityStatus string

const (
	AvailabilityAvailable   AvailabilityStatus = "available"
	AvailabilityUnavailable AvailabilityStatus = "unavailable"
)

type Activation struct {
	Mode     ActivationMode `json:"mode" yaml:"mode"`
	Triggers []string       `json:"triggers" yaml:"triggers"`
}

type Requirements struct {
	Tools         []string `json:"tools" yaml:"tools"`
	OptionalTools []string `json:"optionalTools" yaml:"optional_tools"`
}

type Compatibility struct {
	SciAide string `json:"sciaide" yaml:"sciaide"`
}

type ContextPolicy struct {
	MaxTokens int `json:"maxTokens" yaml:"max_tokens"`
}

type Manifest struct {
	SchemaVersion int           `json:"schemaVersion" yaml:"schema_version"`
	ID            string        `json:"id" yaml:"id"`
	Name          string        `json:"name" yaml:"name"`
	Version       string        `json:"version" yaml:"version"`
	Description   string        `json:"description" yaml:"description"`
	Entry         string        `json:"entry" yaml:"entry"`
	Activation    Activation    `json:"activation" yaml:"activation"`
	Requires      Requirements  `json:"requires" yaml:"requires"`
	Permissions   []string      `json:"permissions" yaml:"permissions"`
	Compatibility Compatibility `json:"compatibility" yaml:"compatibility"`
	Context       ContextPolicy `json:"context" yaml:"context"`
}

// InstalledSkill contains public metadata and integrity provenance. The
// package path is deliberately excluded from JSON so a UI snapshot never
// discloses a local user directory. Instructions are loaded separately only
// when the Agent context explicitly requests an enabled Skill.
type InstalledSkill struct {
	Manifest             Manifest           `json:"manifest"`
	PackageRelativePath  string             `json:"-"`
	ManifestHash         string             `json:"manifestHash"`
	ContentHash          string             `json:"contentHash"`
	PackageHash          string             `json:"packageHash"`
	Integrity            IntegrityStatus    `json:"integrity"`
	IntegrityError       string             `json:"integrityError,omitempty"`
	Availability         AvailabilityStatus `json:"availability"`
	AvailabilityReason   string             `json:"availabilityReason,omitempty"`
	MissingRequiredTools []string           `json:"missingRequiredTools"`
	MissingOptionalTools []string           `json:"missingOptionalTools"`
	Source               PackageSource      `json:"source"`
	InstalledAt          time.Time          `json:"installedAt"`
	UpdatedAt            time.Time          `json:"updatedAt"`
}

type SourceKind string

const (
	SourceFolder  SourceKind = "folder"
	SourceZIP     SourceKind = "zip"
	SourceBuiltin SourceKind = "builtin"
)

type PackageSource struct {
	Kind                SourceKind `json:"kind,omitempty"`
	Name                string     `json:"name,omitempty"`
	Hash                string     `json:"hash,omitempty"`
	Archived            bool       `json:"archived"`
	ArchiveRelativePath string     `json:"-"`
}

type Package struct {
	Skill        InstalledSkill
	Instructions string
}

type Diagnostic struct {
	PackageRelativePath string `json:"-"`
	Message             string `json:"message"`
}

type CatalogSnapshot struct {
	Packages    []Package
	Diagnostics []Diagnostic
	SeenPaths   []string
}

type RefreshResult struct {
	Discovered  int          `json:"discovered"`
	Valid       int          `json:"valid"`
	Invalid     int          `json:"invalid"`
	Missing     int          `json:"missing"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type ReconcileResult struct {
	Missing         int
	RejectedChanges []Diagnostic
}

type ProjectSkill struct {
	ProjectID string    `json:"projectId"`
	SkillID   string    `json:"skillId"`
	Version   string    `json:"version"`
	Enabled   bool      `json:"enabled"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ProjectSkillView struct {
	ProjectSkill
	Skill InstalledSkill `json:"skill"`
}

type SetProjectSkillCommand struct {
	ProjectID string `json:"projectId"`
	SkillID   string `json:"skillId"`
	Version   string `json:"version"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
}

type InstallCommand struct {
	SourcePath      string     `json:"sourcePath"`
	SourceKind      SourceKind `json:"sourceKind"`
	ReplaceExisting bool       `json:"replaceExisting"`
}

type InstallResult struct {
	Skill      InstalledSkill `json:"skill"`
	Replaced   bool           `json:"replaced"`
	Idempotent bool           `json:"idempotent"`
}

type UninstallCommand struct {
	SkillID            string `json:"skillId"`
	Version            string `json:"version"`
	RemoveProjectLinks bool   `json:"removeProjectLinks"`
}

type UninstallResult struct {
	SkillID             string `json:"skillId"`
	Version             string `json:"version"`
	RemovedProjectLinks int    `json:"removedProjectLinks"`
	Recoverable         bool   `json:"recoverable"`
}

type RollbackProjectSkillCommand struct {
	ProjectID     string `json:"projectId"`
	SkillID       string `json:"skillId"`
	TargetVersion string `json:"targetVersion"`
}

type RollbackProjectSkillResult struct {
	FromVersion string           `json:"fromVersion"`
	ToVersion   string           `json:"toVersion"`
	Selection   ProjectSkillView `json:"selection"`
}

type PackageInstallOperation struct {
	Package              Package
	Source               PackageSource
	Replaced             bool
	Idempotent           bool
	InstalledPath        string `json:"-"`
	PreviousPath         string `json:"-"`
	SourceArchivePath    string `json:"-"`
	SourceArchiveCreated bool   `json:"-"`
}

type PackageUninstallOperation struct {
	Moved         bool
	InstalledPath string `json:"-"`
	ArchivedPath  string `json:"-"`
}

type Repository interface {
	Reconcile(ctx context.Context, packages []InstalledSkill, diagnostics []Diagnostic, seenPaths []string, at time.Time) (ReconcileResult, error)
	ListInstalled(ctx context.Context) ([]InstalledSkill, error)
	GetInstalled(ctx context.Context, id, version string) (InstalledSkill, error)
	SetProjectSkill(ctx context.Context, value ProjectSkill) error
	GetProjectSkill(ctx context.Context, projectID, skillID string) (ProjectSkill, error)
	ListProjectSkills(ctx context.Context, projectID string) ([]ProjectSkill, error)
	AcceptInstalled(ctx context.Context, value InstalledSkill, source PackageSource, at time.Time) error
	CountProjectSkillReferences(ctx context.Context, id, version string) (int, error)
	RemoveInstalled(ctx context.Context, id, version string, removeProjectLinks bool) (int, error)
}

type Catalog interface {
	Discover(ctx context.Context) (CatalogSnapshot, error)
	Load(ctx context.Context, packageRelativePath, expectedPackageHash string) (Package, error)
}

type ToolAvailability interface {
	AvailableToolNames(ctx context.Context) ([]string, error)
}

type PackageStore interface {
	Install(ctx context.Context, command InstallCommand) (PackageInstallOperation, error)
	RollbackInstall(ctx context.Context, operation PackageInstallOperation) error
	Uninstall(ctx context.Context, value InstalledSkill) (PackageUninstallOperation, error)
	RollbackUninstall(ctx context.Context, operation PackageUninstallOperation) error
}

type Service struct {
	repository  Repository
	catalog     Catalog
	tools       ToolAvailability
	version     string
	now         func() time.Time
	packages    PackageStore
	operationMu sync.Mutex
}

func NewService(repository Repository, catalog Catalog, tools ToolAvailability, applicationVersion string) *Service {
	return &Service{repository: repository, catalog: catalog, tools: tools, version: applicationVersion, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetPackageStore(store PackageStore) error {
	if store == nil {
		return fmt.Errorf("Skill package store is required")
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	s.packages = store
	return nil
}

func (s *Service) Refresh(ctx context.Context) (RefreshResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.refresh(ctx)
}

func (s *Service) refresh(ctx context.Context) (RefreshResult, error) {
	if s.repository == nil || s.catalog == nil {
		return RefreshResult{}, fmt.Errorf("Skill repository and catalog are required")
	}
	snapshot, err := s.catalog.Discover(ctx)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("discover Skill packages: %w", err)
	}
	values := make([]InstalledSkill, len(snapshot.Packages))
	for index := range snapshot.Packages {
		values[index] = snapshot.Packages[index].Skill
	}
	reconciled, err := s.repository.Reconcile(ctx, values, snapshot.Diagnostics, snapshot.SeenPaths, s.now())
	if err != nil {
		return RefreshResult{}, fmt.Errorf("reconcile Skill catalog: %w", err)
	}
	diagnostics := append([]Diagnostic{}, snapshot.Diagnostics...)
	diagnostics = append(diagnostics, reconciled.RejectedChanges...)
	return RefreshResult{Discovered: len(snapshot.SeenPaths), Valid: len(values) - len(reconciled.RejectedChanges), Invalid: len(diagnostics), Missing: reconciled.Missing, Diagnostics: publicDiagnostics(diagnostics)}, nil
}

func (s *Service) ListInstalled(ctx context.Context) ([]InstalledSkill, error) {
	values, err := s.repository.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	available, err := s.availableTools(ctx)
	if err != nil {
		return nil, err
	}
	for index := range values {
		evaluateAvailability(&values[index], s.version, available)
	}
	return values, nil
}

func (s *Service) SetProjectSkill(ctx context.Context, command SetProjectSkillCommand) (ProjectSkillView, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.setProjectSkill(ctx, command)
}

func (s *Service) setProjectSkill(ctx context.Context, command SetProjectSkillCommand) (ProjectSkillView, error) {
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.SkillID = strings.TrimSpace(command.SkillID)
	command.Version = strings.TrimSpace(command.Version)
	if command.ProjectID == "" || !ValidID(command.SkillID) || !ValidVersion(command.Version) {
		return ProjectSkillView{}, fmt.Errorf("project, Skill id and version are required")
	}
	if command.Priority < 0 || command.Priority > 1000 {
		return ProjectSkillView{}, fmt.Errorf("Skill priority must be between 0 and 1000")
	}
	installed, err := s.repository.GetInstalled(ctx, command.SkillID, command.Version)
	if err != nil {
		return ProjectSkillView{}, err
	}
	available, err := s.availableTools(ctx)
	if err != nil {
		return ProjectSkillView{}, err
	}
	evaluateAvailability(&installed, s.version, available)
	if command.Enabled && installed.Availability != AvailabilityAvailable {
		return ProjectSkillView{}, fmt.Errorf("Skill is unavailable: %s", installed.AvailabilityReason)
	}
	now := s.now()
	value := ProjectSkill{ProjectID: command.ProjectID, SkillID: command.SkillID, Version: command.Version, Enabled: command.Enabled, Priority: command.Priority, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.SetProjectSkill(ctx, value); err != nil {
		return ProjectSkillView{}, err
	}
	persisted, err := s.repository.GetProjectSkill(ctx, command.ProjectID, command.SkillID)
	if err != nil {
		return ProjectSkillView{}, err
	}
	return ProjectSkillView{ProjectSkill: persisted, Skill: installed}, nil
}

func (s *Service) Install(ctx context.Context, command InstallCommand) (InstallResult, error) {
	command.SourcePath = strings.TrimSpace(command.SourcePath)
	if command.SourcePath == "" || command.SourceKind != SourceFolder && command.SourceKind != SourceZIP {
		return InstallResult{}, fmt.Errorf("Skill source path and source kind are required")
	}
	return s.install(ctx, command, PackageSource{})
}

// InstallBuiltin uses the same staging, validation, source archival and
// persistence path as a user installation. Only bootstrap code can call this
// method; the public Install command deliberately rejects SourceBuiltin.
func (s *Service) InstallBuiltin(ctx context.Context, sourcePath string) (InstallResult, error) {
	command := InstallCommand{SourcePath: strings.TrimSpace(sourcePath), SourceKind: SourceFolder}
	if command.SourcePath == "" {
		return InstallResult{}, fmt.Errorf("built-in Skill source path is required")
	}
	return s.install(ctx, command, PackageSource{Kind: SourceBuiltin, Name: "SciAide built-in"})
}

func (s *Service) install(ctx context.Context, command InstallCommand, sourceOverride PackageSource) (InstallResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.packages == nil {
		return InstallResult{}, fmt.Errorf("Skill package installation is not configured")
	}
	operation, err := s.packages.Install(ctx, command)
	if err != nil {
		return InstallResult{}, err
	}
	if sourceOverride.Kind != "" {
		operation.Source.Kind = sourceOverride.Kind
		operation.Source.Name = sourceOverride.Name
	}
	rollback := func(cause error) (InstallResult, error) {
		if rollbackErr := s.packages.RollbackInstall(context.Background(), operation); rollbackErr != nil {
			return InstallResult{}, fmt.Errorf("%w; rollback installed files: %v", cause, rollbackErr)
		}
		return InstallResult{}, cause
	}
	existing, err := s.repository.GetInstalled(ctx, operation.Package.Skill.Manifest.ID, operation.Package.Skill.Manifest.Version)
	if err != nil && !errors.Is(err, ErrSkillNotFound) {
		return rollback(fmt.Errorf("read existing Skill provenance: %w", err))
	}
	recorded := err == nil
	provenanceChanged := recorded && existing.PackageHash != operation.Package.Skill.PackageHash
	if provenanceChanged && !command.ReplaceExisting {
		return rollback(fmt.Errorf("Skill %s@%s is already recorded with different content; explicit replacement is required", existing.Manifest.ID, existing.Manifest.Version))
	}
	now := s.now()
	value := operation.Package.Skill
	value.Source = operation.Source
	value.InstalledAt = now
	value.UpdatedAt = now
	available, err := s.availableTools(ctx)
	if err != nil {
		return rollback(err)
	}
	if err := s.repository.AcceptInstalled(ctx, value, operation.Source, now); err != nil {
		return rollback(fmt.Errorf("persist installed Skill: %w", err))
	}
	persisted, err := s.repository.GetInstalled(ctx, value.Manifest.ID, value.Manifest.Version)
	if err != nil {
		return InstallResult{}, fmt.Errorf("reload installed Skill: %w", err)
	}
	evaluateAvailability(&persisted, s.version, available)
	return InstallResult{Skill: persisted, Replaced: operation.Replaced || provenanceChanged, Idempotent: operation.Idempotent && recorded && !provenanceChanged}, nil
}

func (s *Service) Uninstall(ctx context.Context, command UninstallCommand) (UninstallResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.packages == nil {
		return UninstallResult{}, fmt.Errorf("Skill package uninstallation is not configured")
	}
	command.SkillID = strings.TrimSpace(command.SkillID)
	command.Version = strings.TrimSpace(command.Version)
	if !ValidID(command.SkillID) || !ValidVersion(command.Version) {
		return UninstallResult{}, fmt.Errorf("valid Skill id and version are required")
	}
	installed, err := s.repository.GetInstalled(ctx, command.SkillID, command.Version)
	if err != nil {
		return UninstallResult{}, err
	}
	if installed.Source.Kind == SourceBuiltin {
		return UninstallResult{}, fmt.Errorf("built-in Skill cannot be uninstalled; disable it for the project or explicitly replace it with a user package")
	}
	references, err := s.repository.CountProjectSkillReferences(ctx, command.SkillID, command.Version)
	if err != nil {
		return UninstallResult{}, err
	}
	if references > 0 && !command.RemoveProjectLinks {
		return UninstallResult{}, fmt.Errorf("Skill %s@%s is referenced by %d project(s); switch those projects to another version or explicitly remove the links", command.SkillID, command.Version, references)
	}
	operation, err := s.packages.Uninstall(ctx, installed)
	if err != nil {
		return UninstallResult{}, err
	}
	removed, err := s.repository.RemoveInstalled(ctx, command.SkillID, command.Version, command.RemoveProjectLinks)
	if err != nil {
		if rollbackErr := s.packages.RollbackUninstall(context.Background(), operation); rollbackErr != nil {
			return UninstallResult{}, fmt.Errorf("remove installed Skill: %w; restore package files: %v", err, rollbackErr)
		}
		return UninstallResult{}, fmt.Errorf("remove installed Skill: %w", err)
	}
	return UninstallResult{SkillID: command.SkillID, Version: command.Version, RemovedProjectLinks: removed, Recoverable: operation.Moved}, nil
}

func (s *Service) RollbackProjectSkill(ctx context.Context, command RollbackProjectSkillCommand) (RollbackProjectSkillResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	command.SkillID = strings.TrimSpace(command.SkillID)
	command.TargetVersion = strings.TrimSpace(command.TargetVersion)
	if command.ProjectID == "" || !ValidID(command.SkillID) || !ValidVersion(command.TargetVersion) {
		return RollbackProjectSkillResult{}, fmt.Errorf("project, Skill id and target version are required")
	}
	current, err := s.repository.GetProjectSkill(ctx, command.ProjectID, command.SkillID)
	if err != nil {
		return RollbackProjectSkillResult{}, err
	}
	if compareVersionMust(command.TargetVersion, current.Version) >= 0 {
		return RollbackProjectSkillResult{}, fmt.Errorf("rollback target %s must be older than current version %s", command.TargetVersion, current.Version)
	}
	selection, err := s.setProjectSkill(ctx, SetProjectSkillCommand{ProjectID: current.ProjectID, SkillID: current.SkillID, Version: command.TargetVersion, Enabled: current.Enabled, Priority: current.Priority})
	if err != nil {
		return RollbackProjectSkillResult{}, err
	}
	return RollbackProjectSkillResult{FromVersion: current.Version, ToVersion: command.TargetVersion, Selection: selection}, nil
}

func (s *Service) ListProjectSkills(ctx context.Context, projectID string) ([]ProjectSkillView, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	links, err := s.repository.ListProjectSkills(ctx, projectID)
	if err != nil {
		return nil, err
	}
	available, err := s.availableTools(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ProjectSkillView, 0, len(links))
	for _, link := range links {
		installed, err := s.repository.GetInstalled(ctx, link.SkillID, link.Version)
		if err != nil {
			return nil, err
		}
		evaluateAvailability(&installed, s.version, available)
		result = append(result, ProjectSkillView{ProjectSkill: link, Skill: installed})
	}
	return result, nil
}

func (s *Service) LoadEnabled(ctx context.Context, projectID string) ([]Package, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	links, err := s.ListProjectSkills(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Package, 0, len(links))
	for _, link := range links {
		if !link.Enabled {
			continue
		}
		if link.Skill.Availability != AvailabilityAvailable {
			return nil, fmt.Errorf("enabled Skill %s@%s is unavailable: %s", link.SkillID, link.Version, link.Skill.AvailabilityReason)
		}
		loaded, err := s.catalog.Load(ctx, link.Skill.PackageRelativePath, link.Skill.PackageHash)
		if err != nil {
			return nil, fmt.Errorf("load enabled Skill %s@%s: %w", link.SkillID, link.Version, err)
		}
		// Catalog.Load proves the current bytes still match the trusted hash;
		// repository metadata remains the source of install timestamps and the
		// availability decision used for this project.
		loaded.Skill = link.Skill
		result = append(result, loaded)
	}
	return result, nil
}

func (s *Service) availableTools(ctx context.Context) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	if s.tools == nil {
		return result, nil
	}
	values, err := s.tools.AvailableToolNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list available tools: %w", err)
	}
	for _, value := range values {
		result[strings.TrimSpace(value)] = struct{}{}
	}
	return result, nil
}

func evaluateAvailability(value *InstalledSkill, applicationVersion string, tools map[string]struct{}) {
	value.MissingRequiredTools = []string{}
	value.MissingOptionalTools = []string{}
	if value.Integrity != IntegrityValid {
		value.Availability = AvailabilityUnavailable
		value.AvailabilityReason = "Skill package integrity is " + string(value.Integrity)
		if value.IntegrityError != "" {
			value.AvailabilityReason = value.IntegrityError
		}
		return
	}
	compatible, err := SatisfiesVersion(applicationVersion, value.Manifest.Compatibility.SciAide)
	if err != nil || !compatible {
		value.Availability = AvailabilityUnavailable
		value.AvailabilityReason = "Skill is incompatible with SciAide " + applicationVersion
		return
	}
	for _, name := range value.Manifest.Requires.Tools {
		if _, exists := tools[name]; !exists {
			value.MissingRequiredTools = append(value.MissingRequiredTools, name)
		}
	}
	for _, name := range value.Manifest.Requires.OptionalTools {
		if _, exists := tools[name]; !exists {
			value.MissingOptionalTools = append(value.MissingOptionalTools, name)
		}
	}
	if len(value.MissingRequiredTools) > 0 {
		value.Availability = AvailabilityUnavailable
		value.AvailabilityReason = "required tools are unavailable: " + strings.Join(value.MissingRequiredTools, ", ")
		return
	}
	value.Availability = AvailabilityAvailable
	value.AvailabilityReason = ""
}

func NormalizeManifest(value Manifest) Manifest {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = normalizeSingleLine(value.Name)
	value.Version = strings.TrimSpace(value.Version)
	value.Description = normalizeSingleLine(value.Description)
	value.Entry = strings.TrimSpace(value.Entry)
	value.Activation.Mode = ActivationMode(strings.TrimSpace(string(value.Activation.Mode)))
	if value.Activation.Mode == "" {
		value.Activation.Mode = ActivationExplicit
	}
	value.Activation.Triggers = normalizeList(value.Activation.Triggers)
	value.Requires.Tools = normalizeList(value.Requires.Tools)
	value.Requires.OptionalTools = normalizeList(value.Requires.OptionalTools)
	value.Permissions = normalizeList(value.Permissions)
	value.Compatibility.SciAide = strings.TrimSpace(value.Compatibility.SciAide)
	if value.Context.MaxTokens == 0 {
		value.Context.MaxTokens = DefaultContextTokens
	}
	return value
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
var toolPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

func ValidID(value string) bool { return idPattern.MatchString(value) }
func ValidVersion(value string) bool {
	_, err := parseVersion(value)
	return err == nil
}

func ValidateManifest(value Manifest) error {
	if value.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported Skill schema version %d", value.SchemaVersion)
	}
	if !ValidID(value.ID) || !ValidVersion(value.Version) || !validText(value.Version, 5, 64) {
		return fmt.Errorf("invalid Skill id or semantic version")
	}
	if value.Entry != "SKILL.md" {
		return fmt.Errorf("Skill entry must be SKILL.md")
	}
	if !validText(value.Name, 1, 100) || !validText(value.Description, 1, 500) {
		return fmt.Errorf("Skill name or description is invalid")
	}
	if value.Activation.Mode != ActivationExplicit && value.Activation.Mode != ActivationSuggest {
		return fmt.Errorf("invalid Skill activation mode")
	}
	if value.Activation.Mode == ActivationSuggest && len(value.Activation.Triggers) == 0 {
		return fmt.Errorf("suggest activation requires at least one trigger")
	}
	if err := validateUniqueList("activation trigger", value.Activation.Triggers, 50, 100, nil); err != nil {
		return err
	}
	if err := validateUniqueList("required tool", value.Requires.Tools, 64, 255, toolPattern); err != nil {
		return err
	}
	if err := validateUniqueList("optional tool", value.Requires.OptionalTools, 64, 255, toolPattern); err != nil {
		return err
	}
	required := make(map[string]struct{}, len(value.Requires.Tools))
	for _, name := range value.Requires.Tools {
		required[name] = struct{}{}
	}
	for _, name := range value.Requires.OptionalTools {
		if _, duplicate := required[name]; duplicate {
			return fmt.Errorf("tool %q cannot be both required and optional", name)
		}
	}
	allowedPermissions := map[string]struct{}{"workspace.read": {}, "workspace.write": {}, "filesystem.external": {}, "network.domain": {}, "process.execute": {}, "destructive": {}, "secret.use": {}}
	if err := validateUniqueList("permission", value.Permissions, len(allowedPermissions), 64, nil); err != nil {
		return err
	}
	for _, permission := range value.Permissions {
		if _, exists := allowedPermissions[permission]; !exists {
			return fmt.Errorf("unsupported Skill permission %q", permission)
		}
	}
	if value.Context.MaxTokens < 256 || value.Context.MaxTokens > MaxContextTokens {
		return fmt.Errorf("Skill context max_tokens must be between 256 and %d", MaxContextTokens)
	}
	if !validText(value.Compatibility.SciAide, 1, 256) {
		return fmt.Errorf("Skill compatibility.sciaide is required")
	}
	if _, err := parseConstraints(value.Compatibility.SciAide); err != nil {
		return fmt.Errorf("invalid SciAide compatibility constraint: %w", err)
	}
	return nil
}

func CanonicalManifest(value Manifest) ([]byte, error) {
	return json.Marshal(value)
}

func validText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	length := len([]rune(value))
	return length >= minimum && length <= maximum
}

func validateUniqueList(label string, values []string, maximumItems, maximumRunes int, pattern *regexp.Regexp) error {
	if len(values) > maximumItems {
		return fmt.Errorf("too many %ss", label)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validText(value, 1, maximumRunes) || (pattern != nil && !pattern.MatchString(value)) {
			return fmt.Errorf("invalid %s %q", label, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func normalizeList(values []string) []string {
	if values == nil {
		return []string{}
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = normalizeSingleLine(value)
	}
	return result
}

func normalizeSingleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func publicDiagnostics(values []Diagnostic) []Diagnostic {
	result := make([]Diagnostic, len(values))
	for index, value := range values {
		message := strings.TrimSpace(value.Message)
		if len([]rune(message)) > 500 {
			message = string([]rune(message)[:500])
		}
		result[index] = Diagnostic{Message: message}
	}
	return result
}

type semanticVersion struct {
	major, minor, patch int
	prerelease          string
}

type versionConstraint struct {
	op      string
	version semanticVersion
}

func parseVersion(value string) (semanticVersion, error) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return semanticVersion{}, fmt.Errorf("invalid semantic version %q", value)
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return semanticVersion{}, fmt.Errorf("semantic version major is too large")
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return semanticVersion{}, fmt.Errorf("semantic version minor is too large")
	}
	patch, err := strconv.Atoi(match[3])
	if err != nil {
		return semanticVersion{}, fmt.Errorf("semantic version patch is too large")
	}
	prerelease := ""
	base := strings.SplitN(strings.SplitN(strings.TrimSpace(value), "+", 2)[0], "-", 2)
	if len(base) == 2 {
		prerelease = base[1]
		for _, identifier := range strings.Split(prerelease, ".") {
			if numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semanticVersion{}, fmt.Errorf("numeric prerelease identifiers cannot contain leading zeroes")
			}
		}
	}
	return semanticVersion{major: major, minor: minor, patch: patch, prerelease: prerelease}, nil
}

func parseConstraints(value string) ([]versionConstraint, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 8 {
		return nil, fmt.Errorf("constraint is empty or too complex")
	}
	result := make([]versionConstraint, 0, len(fields))
	for _, field := range fields {
		op := "="
		versionText := field
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(field, candidate) {
				op, versionText = candidate, strings.TrimPrefix(field, candidate)
				break
			}
		}
		version, err := parseVersion(versionText)
		if err != nil {
			return nil, err
		}
		result = append(result, versionConstraint{op: op, version: version})
	}
	return result, nil
}

func SatisfiesVersion(applicationVersion, constraint string) (bool, error) {
	current, err := parseVersion(applicationVersion)
	if err != nil {
		return false, err
	}
	constraints, err := parseConstraints(constraint)
	if err != nil {
		return false, err
	}
	for _, item := range constraints {
		comparison := compareVersion(current, item.version)
		matches := item.op == "=" && comparison == 0 || item.op == ">" && comparison > 0 || item.op == ">=" && comparison >= 0 || item.op == "<" && comparison < 0 || item.op == "<=" && comparison <= 0
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func compareVersion(left, right semanticVersion) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if left.prerelease == right.prerelease {
		return 0
	}
	if left.prerelease == "" {
		return 1
	}
	if right.prerelease == "" {
		return -1
	}
	leftParts, rightParts := strings.Split(left.prerelease, "."), strings.Split(right.prerelease, ".")
	for index := 0; index < len(leftParts) && index < len(rightParts); index++ {
		if leftParts[index] == rightParts[index] {
			continue
		}
		leftNumeric, rightNumeric := numericIdentifier(leftParts[index]), numericIdentifier(rightParts[index])
		if leftNumeric && rightNumeric {
			if len(leftParts[index]) < len(rightParts[index]) {
				return -1
			}
			if len(leftParts[index]) > len(rightParts[index]) {
				return 1
			}
			if leftParts[index] < rightParts[index] {
				return -1
			}
			return 1
		}
		if leftNumeric {
			return -1
		}
		if rightNumeric {
			return 1
		}
		return strings.Compare(leftParts[index], rightParts[index])
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	return 1
}

func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func SortInstalled(values []InstalledSkill) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Manifest.ID == values[j].Manifest.ID {
			return compareVersionMust(values[i].Manifest.Version, values[j].Manifest.Version) > 0
		}
		return values[i].Manifest.ID < values[j].Manifest.ID
	})
}

func compareVersionMust(left, right string) int {
	l, errLeft := parseVersion(left)
	r, errRight := parseVersion(right)
	if errLeft != nil || errRight != nil {
		return strings.Compare(left, right)
	}
	return compareVersion(l, r)
}

package opensciskill

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/network"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/platform/filepublish"
)

const (
	installLedgerName  = ".openscience-install.json"
	entryManifestName  = "openscience-skills.json"
	maxInstallFiles    = 10_000
	maxInstallBytes    = 128 * 1024 * 1024
	maxInstallFileSize = 32 * 1024 * 1024
	gitInstallTimeout  = 2 * time.Minute
)

type InstallGitRequest struct {
	URL           string `json:"url"`
	Replace       bool   `json:"replace"`
	AllowWarnings bool   `json:"allowWarnings"`
	ExpectedSHA   string `json:"expectedSha,omitempty"`
}

type InstallRecord struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Verdict     string `json:"verdict"`
	PackageHash string `json:"packageHash"`
}

type ReviewRejection struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type ReviewWarning struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Pattern string `json:"pattern"`
	Snippet string `json:"snippet"`
}

type InstallGitResult struct {
	Namespace      string            `json:"namespace"`
	RepoURL        string            `json:"repoUrl"`
	PinnedSHA      string            `json:"pinnedSha"`
	Installed      []InstallRecord   `json:"installed"`
	Rejected       []ReviewRejection `json:"rejected"`
	Warnings       []ReviewWarning   `json:"warnings"`
	ReviewRequired bool              `json:"reviewRequired"`
	Replaced       bool              `json:"replaced"`
	Idempotent     bool              `json:"idempotent"`
}

type RemoveInstalledResult struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name,omitempty"`
	Archived    int    `json:"archived"`
	Recoverable bool   `json:"recoverable"`
}

type installLedger struct {
	SchemaVersion int                  `json:"schema_version"`
	Namespace     string               `json:"namespace"`
	RepoURL       string               `json:"repo_url"`
	PinnedSHA     string               `json:"pinned_sha"`
	InstalledAt   string               `json:"installed_at"`
	Skills        []installLedgerSkill `json:"skills"`
}

type installLedgerSkill struct {
	Name        string `json:"name"`
	Directory   string `json:"directory,omitempty"`
	Description string `json:"description"`
	Verdict     string `json:"verdict"`
	PackageHash string `json:"package_hash"`
}

type gitSource struct {
	Namespace string
	CloneURL  string
	Ref       string
	Path      string
}

type reviewedPackage struct {
	SourceDir string
	DirName   string
	Info      Info
	Warnings  []ReviewWarning
}

var (
	githubShorthand           = regexp.MustCompile(`^gh:([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?(?:@([^/]+))?(?:/(.+))?$`)
	gitSHA                    = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	suspiciousInstallPatterns = []struct {
		expression *regexp.Regexp
		label      string
	}{
		{regexp.MustCompile(`(?i)\brm\s+-rf\s+(~|\$HOME|/home(?:\b|/))`), "home-dir rm -rf"},
		{regexp.MustCompile(`(?i)\bcurl\b[^\n|]*\|\s*(sh|bash)\b`), "curl pipe shell"},
		{regexp.MustCompile(`(?i)\bwget\b[^\n|]*\|\s*(sh|bash)\b`), "wget pipe shell"},
		{regexp.MustCompile(`(?i)~[/\\]\.ssh\b`), "SSH credential path"},
		{regexp.MustCompile(`(?i)~[/\\]\.aws\b`), "AWS credential path"},
		{regexp.MustCompile(`(?i)~[/\\]\.kube[/\\]config\b`), "Kubernetes credential path"},
		{regexp.MustCompile(`(?i)\beval\s+(?:\$\(|\x60)`), "dynamic shell eval"},
		{regexp.MustCompile(`(?i)\bbase64\b[^\n|]*-d[^\n|]*\|\s*(sh|bash)\b`), "decoded shell pipeline"},
	}
)

func (s *Service) InstallGit(ctx context.Context, request InstallGitRequest) (InstallGitResult, error) {
	source, err := parseGitSource(request.URL)
	if err != nil {
		return emptyInstallResult(), err
	}
	return s.installGitSource(ctx, source, request)
}

func (s *Service) installGitSource(ctx context.Context, source gitSource, request InstallGitRequest) (InstallGitResult, error) {
	result := emptyInstallResult()
	result.Namespace, result.RepoURL = source.Namespace, source.CloneURL
	cloneCtx, cancel := context.WithTimeout(ctx, gitInstallTimeout)
	defer cancel()
	repository, sha, err := cloneSkillRepository(cloneCtx, source)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(repository)
	result.PinnedSHA = sha
	if expected := strings.ToLower(strings.TrimSpace(request.ExpectedSHA)); expected != "" && expected != sha {
		return result, fmt.Errorf("Skill repository changed from reviewed commit %s to %s; review it again", expected, sha)
	}

	root := filepath.Join(repository, "skills")
	if source.Path != "" {
		root = filepath.Join(repository, filepath.FromSlash(source.Path))
	}
	packages, rejected, err := reviewRepositorySkills(root, source.Namespace)
	if err != nil {
		return result, err
	}
	result.Rejected = append(result.Rejected, rejected...)
	for _, item := range packages {
		result.Warnings = append(result.Warnings, item.Warnings...)
	}
	sort.Slice(result.Warnings, func(i, j int) bool {
		if result.Warnings[i].Name == result.Warnings[j].Name {
			if result.Warnings[i].File == result.Warnings[j].File {
				return result.Warnings[i].Line < result.Warnings[j].Line
			}
			return result.Warnings[i].File < result.Warnings[j].File
		}
		return result.Warnings[i].Name < result.Warnings[j].Name
	})
	if len(packages) == 0 {
		return result, nil
	}
	if len(result.Warnings) > 0 && !request.AllowWarnings {
		result.ReviewRequired = true
		return result, nil
	}
	if len(result.Warnings) > 0 && strings.TrimSpace(request.ExpectedSHA) == "" {
		return result, fmt.Errorf("warning-bearing Skills require confirmation against the reviewed commit SHA")
	}

	stage, err := os.MkdirTemp(s.stagingRoot, ".git-install-*")
	if err != nil {
		return result, fmt.Errorf("create Skill install staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	stagedNamespace := filepath.Join(stage, source.Namespace)
	stagedSkills := filepath.Join(stagedNamespace, "skills")
	if err := os.MkdirAll(stagedSkills, 0o700); err != nil {
		return result, err
	}
	entries, entriesPresent := readEntryManifest(repository)
	ledger := installLedger{SchemaVersion: 1, Namespace: source.Namespace, RepoURL: source.CloneURL, PinnedSHA: sha, InstalledAt: time.Now().UTC().Format(time.RFC3339Nano), Skills: []installLedgerSkill{}}
	for _, item := range packages {
		destination := filepath.Join(stagedSkills, item.DirName)
		if err := copyReviewedTree(item.SourceDir, destination); err != nil {
			return result, fmt.Errorf("stage Skill %q: %w", item.Info.Name, err)
		}
		verdict := "pass"
		if len(item.Warnings) > 0 {
			verdict = "warn"
		}
		ledger.Skills = append(ledger.Skills, installLedgerSkill{Name: item.Info.Name, Directory: item.DirName, Description: item.Info.Description, Verdict: verdict, PackageHash: item.Info.PackageHash})
		result.Installed = append(result.Installed, InstallRecord{Namespace: source.Namespace, Name: item.Info.Name, Description: item.Info.Description, Verdict: verdict, PackageHash: item.Info.PackageHash})
	}
	sort.Slice(ledger.Skills, func(i, j int) bool { return ledger.Skills[i].Name < ledger.Skills[j].Name })
	sort.Slice(result.Installed, func(i, j int) bool { return result.Installed[i].Name < result.Installed[j].Name })
	if entriesPresent {
		data, err := json.MarshalIndent(struct {
			Entries []string `json:"entries"`
		}{Entries: entries}, "", "  ")
		if err != nil {
			return result, err
		}
		if err := os.WriteFile(filepath.Join(stagedNamespace, entryManifestName), append(data, '\n'), 0o600); err != nil {
			return result, err
		}
	}
	copyRepositoryNoticeFiles(repository, stagedNamespace)
	if err := writeLedgerFile(filepath.Join(stagedNamespace, installLedgerName), ledger, false); err != nil {
		return result, err
	}

	target := filepath.Join(s.installedRoot, source.Namespace)
	if err := ensureDirectChild(s.installedRoot, target); err != nil {
		return result, err
	}
	if existing, readErr := readInstallLedger(target); readErr == nil && existing.RepoURL == ledger.RepoURL && existing.PinnedSHA == ledger.PinnedSHA && ledgerMatchesPackages(existing, packages) {
		result.Idempotent = true
		return result, nil
	}
	info, statErr := os.Lstat(target)
	if statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("installed Skill namespace is unsafe")
		}
		if !request.Replace {
			return result, fmt.Errorf("Skill namespace %q already exists with different content; explicit replacement is required", source.Namespace)
		}
	} else if !os.IsNotExist(statErr) {
		return result, statErr
	}

	backup := ""
	if statErr == nil {
		backup, err = s.archivePath("replaced", source.Namespace)
		if err != nil {
			return result, err
		}
		if err := os.Rename(target, backup); err != nil {
			return result, fmt.Errorf("archive existing Skill namespace: %w", err)
		}
		result.Replaced = true
	}
	if err := os.Rename(stagedNamespace, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return result, fmt.Errorf("publish installed Skill namespace: %w", err)
	}
	s.Invalidate()
	return result, nil
}

func emptyInstallResult() InstallGitResult {
	return InstallGitResult{Installed: []InstallRecord{}, Rejected: []ReviewRejection{}, Warnings: []ReviewWarning{}}
}

func (s *Service) RemoveInstalled(_ context.Context, namespace, name string) (RemoveInstalledResult, error) {
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	result := RemoveInstalledResult{Namespace: namespace, Name: name}
	if !ValidName(namespace) || name != "" && !ValidName(name) {
		return result, fmt.Errorf("valid installed Skill namespace and name are required")
	}
	namespaceRoot := filepath.Join(s.installedRoot, namespace)
	if err := ensureDirectChild(s.installedRoot, namespaceRoot); err != nil {
		return result, err
	}
	info, err := os.Lstat(namespaceRoot)
	if os.IsNotExist(err) {
		return result, fmt.Errorf("installed Skill namespace %q not found", namespace)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, fmt.Errorf("installed Skill namespace is unsafe")
	}
	ledger, ledgerErr := readInstallLedger(namespaceRoot)
	if ledgerErr != nil || ledger.Namespace != namespace {
		return result, fmt.Errorf("installed Skill namespace has no valid SciAide install ledger")
	}
	if name == "" {
		backup, err := s.archivePath("removed", namespace)
		if err != nil {
			return result, err
		}
		count := countInstalledSkillDirectories(namespaceRoot)
		if err := os.Rename(namespaceRoot, backup); err != nil {
			return result, err
		}
		result.Archived, result.Recoverable = count, true
		s.Invalidate()
		return result, nil
	}
	directory := ""
	for _, item := range ledger.Skills {
		if item.Name == name {
			directory = item.Directory
			if directory == "" {
				directory = item.Name
			}
			break
		}
	}
	if directory == "" || strings.ContainsAny(directory, `/\\`) {
		return result, fmt.Errorf("installed Skill %q not found in namespace %q", name, namespace)
	}
	target := filepath.Join(namespaceRoot, "skills", directory)
	if err := ensureDirectChild(filepath.Join(namespaceRoot, "skills"), target); err != nil {
		return result, err
	}
	info, err = os.Lstat(target)
	if os.IsNotExist(err) {
		return result, fmt.Errorf("installed Skill %q not found in namespace %q", name, namespace)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, fmt.Errorf("installed Skill directory is unsafe")
	}
	backup, err := s.archivePath("removed", namespace+"-"+name)
	if err != nil {
		return result, err
	}
	if err := os.Rename(target, backup); err != nil {
		return result, err
	}
	kept := ledger.Skills[:0]
	for _, item := range ledger.Skills {
		if item.Name != name {
			kept = append(kept, item)
		}
	}
	ledger.Skills = kept
	if err := writeLedgerFile(filepath.Join(namespaceRoot, installLedgerName), ledger, true); err != nil {
		_ = os.Rename(backup, target)
		return result, err
	}
	result.Archived, result.Recoverable = 1, true
	if countInstalledSkillDirectories(namespaceRoot) == 0 {
		_ = os.RemoveAll(namespaceRoot)
	}
	s.Invalidate()
	return result, nil
}

func (s *Service) enrichInstalled(values []Info) ([]Info, []string) {
	ledgers := map[string]installLedger{}
	entries := map[string]map[string]struct{}{}
	entryDeclared := map[string]bool{}
	diagnostics := []string{}
	for index := range values {
		relative, err := filepath.Rel(s.installedRoot, values[index].root)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) < 2 || !ValidName(parts[0]) {
			continue
		}
		namespace := parts[0]
		values[index].Namespace = namespace
		ledger, loaded := ledgers[namespace]
		if !loaded {
			ledger, err = readInstallLedger(filepath.Join(s.installedRoot, namespace))
			if err == nil {
				ledgers[namespace] = ledger
			} else if !errors.Is(err, fs.ErrNotExist) {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: invalid install ledger: %v", namespace, err))
			}
			manifestEntries, present := readEntryManifest(filepath.Join(s.installedRoot, namespace))
			entryDeclared[namespace] = present
			entrySet := map[string]struct{}{}
			for _, name := range manifestEntries {
				entrySet[name] = struct{}{}
			}
			entries[namespace] = entrySet
		}
		values[index].RepoURL, values[index].PinnedSHA, values[index].InstalledAt = ledger.RepoURL, ledger.PinnedSHA, ledger.InstalledAt
		for _, item := range ledger.Skills {
			if item.Name == values[index].Name {
				values[index].ReviewVerdict = item.Verdict
				break
			}
		}
		if entryDeclared[namespace] {
			_, byName := entries[namespace][values[index].Name]
			_, byDirectory := entries[namespace][filepath.Base(values[index].root)]
			values[index].Entry = byName || byDirectory
		}
	}
	return values, diagnostics
}

func parseGitSource(input string) (gitSource, error) {
	raw := strings.TrimSpace(strings.TrimSuffix(input, "/"))
	if raw == "" || strings.Contains(raw, "%") {
		return gitSource{}, fmt.Errorf("a public HTTPS Git repository URL is required")
	}
	if match := githubShorthand.FindStringSubmatch(raw); match != nil {
		repo := strings.TrimSuffix(match[2], ".git")
		namespace, err := skillNamespace(repo)
		if err != nil {
			return gitSource{}, err
		}
		path, err := cleanRepositorySubpath(match[4])
		if err != nil {
			return gitSource{}, err
		}
		return gitSource{Namespace: namespace, CloneURL: "https://github.com/" + match[1] + "/" + repo + ".git", Ref: strings.TrimSpace(match[3]), Path: path}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return gitSource{}, fmt.Errorf("only public HTTPS Git repository URLs and gh:owner/repo are supported")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return gitSource{}, fmt.Errorf("Git repository URL must use the standard HTTPS port")
	}
	if !publicGitHost(parsed.Hostname()) {
		return gitSource{}, fmt.Errorf("Git repository host must be public")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 {
		return gitSource{}, fmt.Errorf("Git repository URL must include owner and repository")
	}
	repoIndex := len(segments) - 1
	ref, subpath := "", ""
	if strings.EqualFold(parsed.Hostname(), "github.com") && len(segments) >= 4 && segments[2] == "tree" {
		repoIndex = 1
		ref = segments[3]
		if len(segments) > 4 {
			subpath = strings.Join(segments[4:], "/")
		}
	}
	repo := strings.TrimSuffix(segments[repoIndex], ".git")
	namespace, err := skillNamespace(repo)
	if err != nil {
		return gitSource{}, err
	}
	cleanPath, err := cleanRepositorySubpath(subpath)
	if err != nil {
		return gitSource{}, err
	}
	clone := *parsed
	clone.Path, clone.RawPath, clone.RawQuery, clone.Fragment = "/"+strings.Join(segments[:repoIndex+1], "/")+".git", "", "", ""
	return gitSource{Namespace: namespace, CloneURL: clone.String(), Ref: ref, Path: cleanPath}, nil
}

func publicGitHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	return true
}

func skillNamespace(repo string) (string, error) {
	var output strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(repo)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			output.WriteRune(character)
		} else {
			output.WriteByte('-')
		}
	}
	value := strings.Trim(output.String(), "-_")
	if len(value) > 64 {
		value = strings.Trim(value[:64], "-_")
	}
	if !ValidName(value) {
		return "", fmt.Errorf("repository name cannot form a valid Skill namespace")
	}
	return value, nil
}

func cleanRepositorySubpath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", nil
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(value)) {
		return "", fmt.Errorf("Skill repository subpath must stay inside the repository")
	}
	return clean, nil
}

func cloneSkillRepository(ctx context.Context, source gitSource) (string, string, error) {
	directory, err := os.MkdirTemp("", "sciaide-skill-git-*")
	if err != nil {
		return "", "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	arguments := []string{"-c", "credential.helper=", "-c", "core.askPass=", "clone", "--quiet", "--depth", "1", "--no-recurse-submodules"}
	if source.Ref != "" {
		arguments = append(arguments, "--branch", source.Ref)
	}
	arguments = append(arguments, "--", source.CloneURL, directory)
	if err := runGit(ctx, "", arguments...); err != nil {
		return "", "", fmt.Errorf("git clone failed: %w", err)
	}
	var output bytes.Buffer
	if err := runGitOutput(ctx, directory, &output, "rev-parse", "HEAD"); err != nil {
		return "", "", fmt.Errorf("resolve cloned commit: %w", err)
	}
	sha := strings.ToLower(strings.TrimSpace(output.String()))
	if !gitSHA.MatchString(sha) {
		return "", "", fmt.Errorf("Git returned an invalid commit identity")
	}
	cleanup = false
	return directory, sha, nil
}

func runGit(ctx context.Context, directory string, arguments ...string) error {
	return runGitOutput(ctx, directory, io.Discard, arguments...)
}

func runGitOutput(ctx context.Context, directory string, stdout io.Writer, arguments ...string) error {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = network.Environment(gitEnvironment(), "skills")
	proxy, _ := network.Resolve("skills")
	command.Env = append(command.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.proxy", "GIT_CONFIG_VALUE_0="+proxy.URL)
	command.Stdout = stdout
	stderr := &boundedBuffer{remaining: 64 * 1024}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("operation timed out")
		}
		if message != "" {
			return fmt.Errorf("%s", message)
		}
		return err
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if b.remaining > 0 {
		stored := min(len(value), b.remaining)
		_, _ = b.Buffer.Write(value[:stored])
		b.remaining -= stored
	}
	return original, nil
}

func gitEnvironment() []string {
	blocked := map[string]struct{}{"GIT_ASKPASS": {}, "SSH_ASKPASS": {}, "GIT_SSH": {}, "GIT_SSH_COMMAND": {}}
	result := make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if _, skip := blocked[strings.ToUpper(key)]; !skip {
			result = append(result, item)
		}
	}
	return append(result, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never", "GIT_CONFIG_NOSYSTEM=1")
}

func reviewRepositorySkills(root, namespace string) ([]reviewedPackage, []ReviewRejection, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("no Skills found; expected direct child directories under %s", filepath.ToSlash(root))
	}
	if err != nil {
		return nil, nil, err
	}
	packages := []reviewedPackage{}
	rejections := []ReviewRejection{}
	seen := map[string]struct{}{}
	rootFS := os.DirFS(root)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		skillFile := filepath.Join(root, entry.Name(), "SKILL.md")
		contents, err := os.ReadFile(skillFile)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			rejections = append(rejections, ReviewRejection{Name: entry.Name(), Reason: err.Error()})
			continue
		}
		meta, body, err := parseMarkdown(contents)
		if err != nil {
			rejections = append(rejections, ReviewRejection{Name: entry.Name(), Reason: err.Error()})
			continue
		}
		if _, duplicate := seen[meta.Name]; duplicate {
			rejections = append(rejections, ReviewRejection{Name: meta.Name, Reason: "duplicate frontmatter name in repository"})
			continue
		}
		seen[meta.Name] = struct{}{}
		warnings, rejection, err := reviewPackageTree(filepath.Join(root, entry.Name()), meta.Name, meta.Description, body)
		if err != nil {
			return nil, nil, err
		}
		if rejection != "" {
			rejections = append(rejections, ReviewRejection{Name: meta.Name, Reason: rejection})
			continue
		}
		info, err := inspectPackageFS(rootFS, filepath.ToSlash(entry.Name()), filepath.ToSlash(filepath.Join(entry.Name(), "SKILL.md")), OriginInstalled, meta, body, false)
		if err != nil {
			rejections = append(rejections, ReviewRejection{Name: meta.Name, Reason: err.Error()})
			continue
		}
		info.Namespace = namespace
		packages = append(packages, reviewedPackage{SourceDir: filepath.Join(root, entry.Name()), DirName: entry.Name(), Info: info, Warnings: warnings})
	}
	if len(packages) == 0 && len(rejections) == 0 {
		return nil, nil, fmt.Errorf("no Skills found; expected */SKILL.md directly below the repository Skill root")
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Info.Name < packages[j].Info.Name })
	sort.Slice(rejections, func(i, j int) bool { return rejections[i].Name < rejections[j].Name })
	return packages, rejections, nil
}

func reviewPackageTree(root, name, description, body string) ([]ReviewWarning, string, error) {
	files, total := 0, int64(0)
	warnings := []ReviewWarning{}
	rejection := rejectSkillContent(description, body)
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package contains a symbolic link: %s", entry.Name())
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("package contains an unsafe file")
		}
		files++
		total += info.Size()
		if files > maxInstallFiles || total > maxInstallBytes || info.Size() > maxInstallFileSize {
			return fmt.Errorf("package exceeds install size limits")
		}
		if info.Size() > MaxSkillMarkdownSize {
			return nil
		}
		contents, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
			return nil
		}
		text := string(contents)
		if rejection == "" {
			rejection = rejectSkillContent(description, text)
		}
		relative, _ := filepath.Rel(root, file)
		for index, line := range strings.Split(text, "\n") {
			for _, pattern := range suspiciousInstallPatterns {
				if pattern.expression.MatchString(line) {
					snippet := strings.TrimSpace(line)
					if runes := []rune(snippet); len(runes) > 240 {
						snippet = string(runes[:240]) + "..."
					}
					warnings = append(warnings, ReviewWarning{Name: name, File: filepath.ToSlash(relative), Line: index + 1, Pattern: pattern.label, Snippet: snippet})
				}
			}
		}
		return nil
	})
	return warnings, rejection, err
}

func copyReviewedTree(source, destination string) error {
	return filepath.WalkDir(source, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not installable")
		}
		relative, err := filepath.Rel(source, file)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("package path escapes its root")
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		input, err := os.Open(file)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOut, closeIn := output.Close(), input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOut != nil {
			return closeOut
		}
		return closeIn
	})
}

func readEntryManifest(root string) ([]string, bool) {
	contents, err := os.ReadFile(filepath.Join(root, entryManifestName))
	if err != nil || len(contents) > 1024*1024 {
		return nil, false
	}
	var value struct {
		Entries []string `json:"entries"`
	}
	if json.Unmarshal(contents, &value) != nil || value.Entries == nil {
		return nil, false
	}
	return cleanStrings(value.Entries), true
}

func copyRepositoryNoticeFiles(repository, destination string) {
	for _, name := range []string{"LICENSE", "LICENSE.txt", "NOTICE", "NOTICE.txt"} {
		source := filepath.Join(repository, name)
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
			continue
		}
		contents, err := os.ReadFile(source)
		if err == nil {
			_ = os.WriteFile(filepath.Join(destination, name), contents, 0o600)
		}
	}
}

func readInstallLedger(namespaceRoot string) (installLedger, error) {
	contents, err := os.ReadFile(filepath.Join(namespaceRoot, installLedgerName))
	if err != nil {
		return installLedger{}, err
	}
	if len(contents) > 2*1024*1024 {
		return installLedger{}, fmt.Errorf("install ledger is too large")
	}
	var value installLedger
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return installLedger{}, err
	}
	if err := ensureInstallJSONEOF(decoder); err != nil {
		return installLedger{}, err
	}
	if value.SchemaVersion != 1 || !ValidName(value.Namespace) || value.RepoURL == "" || !gitSHA.MatchString(value.PinnedSHA) {
		return installLedger{}, fmt.Errorf("install ledger is invalid")
	}
	seen := map[string]struct{}{}
	for _, item := range value.Skills {
		directory := item.Directory
		if directory == "" {
			directory = item.Name
		}
		if !ValidName(item.Name) || directory == "" || strings.ContainsAny(directory, `/\\`) || item.Description == "" || item.Verdict != "pass" && item.Verdict != "warn" || !validLowerHash(item.PackageHash) {
			return installLedger{}, fmt.Errorf("install ledger contains an invalid Skill record")
		}
		if _, exists := seen[item.Name]; exists {
			return installLedger{}, fmt.Errorf("install ledger contains duplicate Skill records")
		}
		seen[item.Name] = struct{}{}
	}
	return value, nil
}

func ensureInstallJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("install ledger contains trailing JSON")
		}
		return err
	}
	return nil
}

func validLowerHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func writeLedgerFile(target string, ledger installLedger, replace bool) error {
	contents, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if !replace {
		return os.WriteFile(target, append(contents, '\n'), 0o600)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".ledger-*.tmp")
	if err != nil {
		return err
	}
	tmp := temporary.Name()
	defer os.Remove(tmp)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(contents, '\n'))
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return filepublish.Replace(tmp, target)
}

func ledgerMatchesPackages(ledger installLedger, packages []reviewedPackage) bool {
	if len(ledger.Skills) != len(packages) {
		return false
	}
	values := map[string]string{}
	for _, item := range ledger.Skills {
		values[item.Name] = item.PackageHash
	}
	for _, item := range packages {
		if values[item.Info.Name] != item.Info.PackageHash {
			return false
		}
	}
	return true
}

func (s *Service) archivePath(kind, label string) (string, error) {
	root := filepath.Join(s.archiveRoot, kind)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s", time.Now().UTC().Format("20060102T150405.000000000Z"), label, hex.EncodeToString(random[:]))
	return filepath.Join(root, name), nil
}

func countInstalledSkillDirectories(namespaceRoot string) int {
	entries, err := os.ReadDir(filepath.Join(namespaceRoot, "skills"))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			count++
		}
	}
	return count
}

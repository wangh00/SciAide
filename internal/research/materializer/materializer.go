package materializer

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/browserenv"
	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/httpua"
	"github.com/wangh00/SciAide/internal/network"
	"golang.org/x/net/publicsuffix"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/id"
)

const (
	defaultTimeout     = 45 * time.Second
	defaultMaxPDFBytes = 100 << 20
	maxMetadataRunes   = 200_000
)

var fixedFullTextHosts = map[string]map[string]struct{}{
	"arxiv": {
		"arxiv.org": {}, "export.arxiv.org": {},
	},
	"europepmc": {
		"europepmc.org": {}, "www.ebi.ac.uk": {},
	},
	"pubmed": {
		"pmc.ncbi.nlm.nih.gov": {}, "www.ncbi.nlm.nih.gov": {},
	},
	"semantic-scholar": {
		"www.semanticscholar.org": {}, "semanticscholar.org": {},
	},
	"crossref": {"www.nature.com": {}, "jamanetwork.com": {}},
	"openalex": {"www.nature.com": {}, "jamanetwork.com": {}},
}

type Service struct {
	browser         *browserenv.Service
	fullTextTimeout time.Duration
	downloadMu      sync.Mutex
	failures        map[string]cachedFailure
	client          *http.Client
	allowHTTP       bool
	maxPDFBytes     int64
	allowedHosts    map[string]map[string]struct{}
}

func (s *Service) SetBrowser(b *browserenv.Service) { s.browser = b }
func New() *Service {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 60 * time.Second,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	transport.DialContext = safeDialContext(dialer)
	service := &Service{maxPDFBytes: defaultMaxPDFBytes, allowedHosts: cloneAllowedHosts(fixedFullTextHosts)}
	service.client = &http.Client{Timeout: defaultTimeout, Transport: browserhttp.NewScoped(transport, "research"), CheckRedirect: fixedHostRedirect}
	return service
}

func (s *Service) Materialize(ctx context.Context, selected project.Project, candidate appresearch.Candidate, mode appresearch.MaterializeMode) (appresearch.MaterializedCandidate, error) {
	if err := ctx.Err(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if s == nil || s.client == nil {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("research materializer is not configured")
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if selected.ID != candidate.ProjectID {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("research candidate does not belong to the current project")
	}
	if err := ctx.Err(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if mode == appresearch.MaterializeAuto {
		if strings.TrimSpace(candidate.Preferred.Abstract) == "" {
			for _, record := range candidate.Records {
				if strings.TrimSpace(record.Work.Abstract) != "" {
					candidate.Preferred.Abstract = record.Work.Abstract
					break
				}
			}
		}
		if strings.TrimSpace(candidate.Preferred.Abstract) != "" {
			return s.writeMetadata(selected, candidate)
		}
	}
	if mode != appresearch.MaterializeMetadata {
		if targets := s.fullTextTargets(candidate); len(targets) > 0 {
			budget := s.fullTextTimeout
			if budget <= 0 {
				budget = defaultTimeout
				if s.browser != nil && s.browser.Available(ctx, selected.ID) {
					budget = 3 * time.Minute
				}
			}
			fetchCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			var value appresearch.MaterializedCandidate
			var err error
			reasons := []string{}
			for _, target := range targets {
				value, err = s.downloadPDF(fetchCtx, selected, candidate, target.record, target.url)
				if ctx.Err() != nil {
					return value, ctx.Err()
				}
				if errors.Is(fetchCtx.Err(), context.DeadlineExceeded) && err != nil {
					err = &FullTextUnavailableError{Message: "full text acquisition timed out within the per-paper budget"}
				}
				var unavailable *FullTextUnavailableError
				if err == nil || !errors.As(err, &unavailable) {
					break
				}
				u, _ := url.Parse(target.url)
				reasons = append(reasons, u.Hostname()+": "+unavailable.Message)
				if fetchCtx.Err() != nil {
					break
				}
			}
			var unavailable *FullTextUnavailableError
			if errors.As(err, &unavailable) && len(reasons) > 0 {
				err = &FullTextUnavailableError{Message: strings.Join(reasons, "; ")}
			}
			if err != nil && mode == appresearch.MaterializeAuto && ctx.Err() == nil && errors.As(err, &unavailable) {
				value, writeErr := s.writeMetadata(selected, candidate)
				if writeErr != nil {
					return value, writeErr
				}
				value.Warning = unavailable.Error() + "; imported metadata/abstract only, full text was not read"
				return value, nil
			}
			return value, err
		}
		if mode == appresearch.MaterializeFullText {
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: "candidate has no open full text on a fixed trusted research source"}
		}
	}
	return s.writeMetadata(selected, candidate)
}

type FullTextUnavailableError struct{ Message string }

func (e *FullTextUnavailableError) Error() string { return e.Message }

func (e *FullTextUnavailableError) FullTextUnavailable() bool { return true }

type cachedFailure struct {
	until   time.Time
	message string
}

type fullTextTarget struct {
	record appresearch.SourceRecord
	url    string
}

func (s *Service) FullTextAvailability(candidate appresearch.Candidate) string {
	if len(s.fullTextTargets(candidate)) == 0 {
		return "unavailable"
	}
	return "requestable_not_verified"
}

// Only derive a fixed Europe PMC endpoint from a strictly validated PMCID.
func (s *Service) fullTextTargets(candidate appresearch.Candidate) []fullTextTarget {
	result := []fullTextTarget{}
	seen := map[string]bool{}
	add := func(record appresearch.SourceRecord, target string) {
		if len(result) < 3 && !seen[target] && s.allowedTarget(record.Work.SourceID, target) {
			seen[target] = true
			result = append(result, fullTextTarget{record, target})
		}
	}
	if record, target, ok := s.fullTextTarget(candidate); ok {
		add(record, target)
	}
	for _, record := range candidate.Records {
		if record.Work.OpenAccess {
			add(record, record.Work.PDFURL)
			for _, target := range record.Work.PDFURLs {
				add(record, target)
			}
		}
		pmcid := strings.ToUpper(strings.TrimSpace(record.Work.Identifiers.PMCID))
		if len(pmcid) <= 3 || len(pmcid) > 20 || !strings.HasPrefix(pmcid, "PMC") || strings.Trim(pmcid[3:], "0123456789") != "" {
			continue
		}
		record.Work.SourceID = "europepmc"
		add(record, "https://europepmc.org/articles/"+pmcid+"?pdf=render")
	}
	return result
}

func (s *Service) Cleanup(value appresearch.MaterializedCandidate) {
	if strings.TrimSpace(value.Path) != "" {
		_ = os.Remove(value.Path)
	}
}

func (s *Service) fullTextTarget(candidate appresearch.Candidate) (appresearch.SourceRecord, string, bool) {
	records := append([]appresearch.SourceRecord(nil), candidate.Records...)
	priority := map[string]int{"arxiv": 0, "europepmc": 1, "pubmed": 2, "semantic-scholar": 3}
	sort.SliceStable(records, func(i, j int) bool {
		a, aOK := priority[records[i].Work.SourceID]
		b, bOK := priority[records[j].Work.SourceID]
		if !aOK {
			a = 100
		}
		if !bOK {
			b = 100
		}
		if a != b {
			return a < b
		}
		return records[i].ID < records[j].ID
	})
	for _, record := range records {
		target := strings.TrimSpace(record.Work.PDFURL)
		if target == "" || !record.Work.OpenAccess || !s.allowedTarget(record.Work.SourceID, target) {
			continue
		}
		return record, target, true
	}
	return appresearch.SourceRecord{}, "", false
}

func (s *Service) allowedTarget(sourceID, target string) bool {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "https" && !(s.allowHTTP && parsed.Scheme == "http") {
		return false
	}
	if parsed.Port() != "" && parsed.Port() != "443" && !(s.allowHTTP && parsed.Port() != "") {
		return false
	}
	hosts := s.allowedHosts[strings.ToLower(strings.TrimSpace(sourceID))]
	_, allowed := hosts[strings.ToLower(parsed.Hostname())]
	if sourceID == "openalex" {
		for _, hosts := range s.allowedHosts {
			if _, ok := hosts[strings.ToLower(parsed.Hostname())]; ok {
				allowed = true
			}
		}
	}
	return allowed
}

func (s *Service) downloadPDF(ctx context.Context, selected project.Project, candidate appresearch.Candidate, record appresearch.SourceRecord, target string) (appresearch.MaterializedCandidate, error) {
	// Serialize downloads even when a model emits several tool calls together.
	for !s.downloadMu.TryLock() {
		select {
		case <-ctx.Done():
			return appresearch.MaterializedCandidate{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer s.downloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	parsed, _ := url.Parse(target)
	proxy, revision := network.Resolve("research")
	binding := browserenv.Fingerprint(proxy, revision)
	failureKey := binding + target
	hostKey := binding + "host:" + parsed.Hostname()
	for _, key := range []string{failureKey, hostKey} {
		if failure, ok := s.failures[key]; ok && time.Now().Before(failure.until) {
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: failure.message + "; request skipped during cooldown"}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	request.Header.Set("Accept", "application/pdf")
	httpua.Apply(request)
	client := *s.client
	if _, ok := client.Transport.(*browserhttp.Transport); ok {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		if proxy.Mode == "custom" {
			u, _ := url.Parse(proxy.URL)
			base.Proxy = http.ProxyURL(u)
		} else {
			base.DialContext = safeDialContext(&net.Dialer{Timeout: 10 * time.Second})
		}
		tr := browserhttp.NewFixed(base)
		defer tr.CloseIdleConnections()
		client.Transport = tr
	}
	client.Jar, _ = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return appresearch.MaterializedCandidate{}, ctx.Err()
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			s.cacheFailure(failureKey, "open full text request timed out", time.Minute)
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: "open full text request timed out"}
		}
		return appresearch.MaterializedCandidate{}, fmt.Errorf("download open full text from %s: %w", record.Work.SourceID, err)
	}
	// Browser is a single opt-in fallback, only for a positively identified CF challenge.
	if response.StatusCode == 403 && strings.EqualFold(response.Header.Get("Cf-Mitigated"), "challenge") && s.browser != nil {
		response.Body.Close()
		verifyURL := target
		if landing, e := url.Parse(record.Work.LandingURL); e == nil && landing.Scheme == "https" && strings.EqualFold(landing.Hostname(), parsed.Hostname()) {
			verifyURL = landing.String()
		}
		clearance, solveErr := s.browser.Solve(ctx, selected.ID, verifyURL, proxy, revision)
		if solveErr != nil {
			if ctx.Err() != nil {
				return appresearch.MaterializedCandidate{}, ctx.Err()
			}
			message := "Cloudflare 验证未完成：" + solveErr.Error()
			s.cacheFailure(failureKey, message, 5*time.Minute)
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: message}
		}
		current, currentRevision := network.Resolve("research")
		if browserenv.Fingerprint(current, currentRevision) != binding {
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: "网络配置已变化，丢弃浏览器 Cookie，请重新请求"}
		}
		verified, closeTransport, solveErr := clearance.Client(ctx)
		if solveErr != nil {
			s.cacheFailure(failureKey, solveErr.Error(), 5*time.Minute)
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: solveErr.Error()}
		}
		defer closeTransport()
		verified.CheckRedirect = fixedHostRedirect
		response, err = verified.Do(request)
		if err != nil {
			s.cacheFailure(failureKey, "浏览器验证后下载仍失败", time.Minute)
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: "浏览器验证后下载仍失败"}
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == 429 || response.StatusCode == 408 || response.StatusCode == 403 || response.StatusCode == 404 || response.StatusCode >= 500 {
			message := fmt.Sprintf("open full text source %s returned HTTP %d", parsed.Hostname(), response.StatusCode)
			s.cacheFailure(failureKey, message, 5*time.Minute)
			if response.StatusCode == 429 || response.StatusCode >= 500 {
				delay := 5 * time.Minute
				if seconds, parseErr := strconv.Atoi(response.Header.Get("Retry-After")); parseErr == nil && seconds > 0 && seconds <= 86400 {
					if suggested := time.Duration(seconds) * time.Second; suggested > delay {
						delay = suggested
					}
				} else if until, parseErr := http.ParseTime(response.Header.Get("Retry-After")); parseErr == nil && time.Until(until) > delay {
					delay = time.Until(until)
					if delay > 24*time.Hour {
						delay = 24 * time.Hour
					}
				}
				s.cacheFailure(hostKey, message, delay)
			}
			return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: message}
		}
		return appresearch.MaterializedCandidate{}, fmt.Errorf("open full text source returned HTTP %d", response.StatusCode)
	}
	if !s.allowedTarget(record.Work.SourceID, response.Request.URL.String()) {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("open full text redirect escaped its fixed research source")
	}
	if response.ContentLength > s.maxPDFBytes {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("open full text exceeds the configured size limit")
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		message := "open full text returned unexpected MIME type HTML, not a PDF; verification unavailable"
		s.cacheFailure(failureKey, message, 5*time.Minute)
		return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: message}
	}
	if mediaType != "" && mediaType != "application/pdf" && mediaType != "application/octet-stream" {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("open full text returned unexpected MIME type %q", mediaType)
	}
	return s.writePDF(selected, candidate, response.Body)
}

func (s *Service) cacheFailure(key, message string, duration time.Duration) {
	if s.failures == nil {
		s.failures = map[string]cachedFailure{}
	}
	for k, value := range s.failures {
		if time.Now().After(value.until) {
			delete(s.failures, k)
		}
	}
	if len(s.failures) >= 256 {
		return
	}
	s.failures[key] = cachedFailure{until: time.Now().Add(duration), message: message}
}

func (s *Service) writePDF(selected project.Project, candidate appresearch.Candidate, reader io.Reader) (appresearch.MaterializedCandidate, error) {
	path, file, err := createStagingFile(selected, ".pdf")
	if err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, s.maxPDFBytes+1))
	var networkError net.Error
	if copyErr != nil && errors.As(copyErr, &networkError) && networkError.Timeout() {
		return appresearch.MaterializedCandidate{}, &FullTextUnavailableError{Message: "open full text download timed out"}
	}
	if copyErr != nil || written < 5 || written > s.maxPDFBytes {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("read open full text within size limit: %w", copyErr)
	}
	if err := file.Sync(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if err := file.Close(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	header := make([]byte, 5)
	input, err := os.Open(path)
	if err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	_, readErr := io.ReadFull(input, header)
	closeErr := input.Close()
	if readErr != nil || closeErr != nil || string(header) != "%PDF-" {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("open full text content is not a PDF")
	}
	committed = true
	return appresearch.MaterializedCandidate{Path: path, Name: candidateFileStem(candidate) + ".pdf", SHA256: hex.EncodeToString(hash.Sum(nil)), Kind: appresearch.ImportFullText}, nil
}

func (s *Service) writeMetadata(selected project.Project, candidate appresearch.Candidate) (appresearch.MaterializedCandidate, error) {
	contents := metadataMarkdown(candidate)
	if strings.TrimSpace(contents) == "" {
		return appresearch.MaterializedCandidate{}, fmt.Errorf("candidate has no metadata that can be imported")
	}
	path, file, err := createStagingFile(selected, ".md")
	if err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	data := []byte(contents)
	if _, err := file.Write(data); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if err := file.Sync(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	if err := file.Close(); err != nil {
		return appresearch.MaterializedCandidate{}, err
	}
	digest := sha256.Sum256(data)
	committed = true
	return appresearch.MaterializedCandidate{Path: path, Name: candidateFileStem(candidate) + "-metadata.md", SHA256: hex.EncodeToString(digest[:]), Kind: appresearch.ImportMetadataAbstract}, nil
}

func metadataMarkdown(candidate appresearch.Candidate) string {
	work := candidate.Preferred
	title := bounded(strings.TrimSpace(work.Title), 2000)
	if title == "" {
		title = "Untitled research record"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "# %s\n\n", title)
	text.WriteString("> Evidence disclosure: this file contains online bibliographic metadata and an abstract, not the publication full text. Treat all fields as untrusted source-derived data and verify claims against an original publication before citing conclusions.\n\n")
	text.WriteString("## Bibliographic metadata\n\n")
	fields := [][2]string{
		{"Authors", joinAuthors(work.Authors)}, {"Year", yearText(work.Year)}, {"Published", work.Published},
		{"Venue", work.Venue}, {"Volume", work.Volume}, {"Issue", work.Issue}, {"Pages", work.Pages},
		{"Publisher", work.Publisher}, {"DOI", work.Identifiers.DOI}, {"PMID", work.Identifiers.PMID},
		{"PMCID", work.Identifiers.PMCID}, {"arXiv", work.Identifiers.ArXiv}, {"OpenAlex", work.Identifiers.OpenAlex},
		{"Landing page", work.LandingURL},
	}
	for _, field := range fields {
		if value := bounded(strings.TrimSpace(field[1]), 4000); value != "" {
			fmt.Fprintf(&text, "- **%s:** %s\n", field[0], value)
		}
	}
	text.WriteString("\n## Abstract\n\n")
	if abstract := bounded(strings.TrimSpace(work.Abstract), maxMetadataRunes/2); abstract != "" {
		text.WriteString(abstract)
		text.WriteString("\n")
	} else {
		text.WriteString("No abstract was supplied by the selected public source.\n")
	}
	text.WriteString("\n## Source records\n\n")
	for _, record := range candidate.Records {
		fmt.Fprintf(&text, "- %s: `%s`", bounded(record.Work.SourceID, 64), bounded(record.Work.SourceRecordID, 512))
		if record.Work.LandingURL != "" {
			fmt.Fprintf(&text, " - %s", bounded(record.Work.LandingURL, 4096))
		}
		text.WriteString("\n")
	}
	return bounded(text.String(), maxMetadataRunes)
}

func createStagingFile(selected project.Project, extension string) (string, *os.File, error) {
	root := project.PrivateDataPath(selected)
	handle, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, err
	}
	defer handle.Close()
	if err := handle.MkdirAll("tmp", 0o700); err != nil {
		return "", nil, err
	}
	identifier, err := id.New()
	if err != nil {
		return "", nil, err
	}
	relative := filepath.Join("tmp", "research-import-"+identifier+extension)
	file, err := handle.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(root, relative), file, nil
}

func safeDialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("research full text host did not resolve")
		}
		for _, address := range addresses {
			if !publicIP(address.IP) {
				return nil, fmt.Errorf("research full text host resolved to a non-public address")
			}
		}
		var lastErr error
		for _, address := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func publicIP(value net.IP) bool {
	return value != nil && !value.IsUnspecified() && !value.IsLoopback() && !value.IsPrivate() && !value.IsLinkLocalUnicast() && !value.IsLinkLocalMulticast() && !value.IsMulticast()
}

func fixedHostRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 4 {
		return fmt.Errorf("research full text redirected too many times")
	}
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if first.Scheme == "https" && first.Host == "www.nature.com" && strings.HasPrefix(first.Path, "/articles/") && strings.HasSuffix(first.Path, ".pdf") {
		u := request.URL
		if u.User != nil || u.Scheme != "https" || u.Fragment != "" {
			return fmt.Errorf("invalid Nature redirect")
		}
		if u.String() == first.String() {
			return nil
		}
		if u.Host == "idp.nature.com" && (u.Path == "/authorize" || u.Path == "/transit") {
			q, err := url.ParseQuery(u.RawQuery)
			if err == nil && len(q["redirect_uri"]) == 1 && q.Get("redirect_uri") == first.String() {
				if u.Path == "/transit" || (q.Get("client_id") == "grover" && q.Get("response_type") == "cookie") {
					return nil
				}
			}
		}
		return fmt.Errorf("research full text redirected outside the approved Nature session flow")
	}
	if request.URL.Scheme != first.Scheme || !strings.EqualFold(request.URL.Hostname(), first.Hostname()) || request.URL.Port() != first.Port() {
		return fmt.Errorf("research full text redirected outside its fixed host")
	}
	return nil
}

func cloneAllowedHosts(value map[string]map[string]struct{}) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{}, len(value))
	for source, hosts := range value {
		result[source] = make(map[string]struct{}, len(hosts))
		for host := range hosts {
			result[source][host] = struct{}{}
		}
	}
	return result
}

func candidateFileStem(candidate appresearch.Candidate) string {
	identifier := strings.ReplaceAll(candidate.ID, "-", "")
	if len(identifier) > 12 {
		identifier = identifier[:12]
	}
	if identifier == "" {
		identifier = "candidate"
	}
	return "research-" + identifier
}

func joinAuthors(values []appresearch.Author) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		if name := strings.TrimSpace(value.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "; ")
}

func yearText(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprint(value)
}

func bounded(value string, maximum int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}

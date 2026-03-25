package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"

	"github.com/simonfxr/gr/pkg/config"
)

type LocalRepo struct {
	Root     string          // absolute repository root directory (worktree), respects -C
	GitDir   string          // absolute path to /../.git dir (resolved .git file if inside worktree)
	Worktree string          // absolute path to /../.git/worktrees/<name>
	GitRepo  *git.Repository // handle to git repository instance or nil if not available
}

// Info contains parsed repository and provider details inferred from git remotes.
type Info struct {
	LocalRepo

	Provider Provider // enum: github|gitlab|bitbucket|unknown
	Variant  string   // cloud|self-hosted|unknown
	Evidence string   // method used (e.g. api_v4_version, headers, url-heuristic)
	HTTPBase string   // http(s) base used for probing, if any

	Host   string // e.g. github.com
	Owner  string // e.g. user or project
	Repo   string // repository name without .git
	Remote string // remote name used (e.g. origin)
	URL    string // original remote URL used

	Config *config.Config // user configuration (may be nil)
}

// DetectFromRepo infers provider and repo info for an already detected local repository
// by inspecting remotes (preferring origin, then upstream, then first). It also
// performs light network probing (like the Python reference) to identify self-hosted services.
func DetectFromRepo(localRepo *LocalRepo, cfg *config.Config) (*Info, error) {
	if localRepo == nil || localRepo.GitRepo == nil {
		return nil, fmt.Errorf("local repo not available")
	}

	repo := localRepo.GitRepo
	// Try go-git first
	remotes, err := repo.Remotes()
	if err != nil || len(remotes) == 0 {
		return nil, fmt.Errorf("no git remotes found")
	}

	// Choose remote: origin > upstream > first
	var chosen = remotes[0]
	for _, r := range remotes {
		if r.Config() != nil && r.Config().Name == "origin" {
			chosen = r
			break
		}
		if r.Config() != nil && r.Config().Name == "upstream" {
			chosen = r // keep as fallback if no origin appears later
		}
	}

	remoteCfg := chosen.Config()
	if remoteCfg == nil || len(remoteCfg.URLs) == 0 {
		return nil, fmt.Errorf("remote %q has no URLs", chosen)
	}
	raw := remoteCfg.URLs[0]

	host, port, owner, repoName := parseRemoteURL(raw)

	svc, variant, evidence, base := detectService(host, port)

	return &Info{
		LocalRepo: *localRepo,
		Provider:  svc,
		Variant:   variant,
		Evidence:  evidence,
		HTTPBase:  base,
		Host:      hostOnly(host),
		Owner:     owner,
		Repo:      repoName,
		Remote:    remoteCfg.Name,
		URL:       raw,
		Config:    cfg,
	}, nil
}

func findDotGit(path string) (dot string, fi os.FileInfo, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fi, err
	}

	for {
		pathinfo, err := os.Stat(path)
		if err != nil {
			return "", fi, err
		}
		if !pathinfo.IsDir() {
			oldpath := path
			path = filepath.Dir(path)
			if oldpath == path {
				return "", fi, os.ErrNotExist
			}
			continue
		}

		dot = filepath.Join(path, ".git")
		fi, err := os.Stat(dot)
		if err == nil {
			return dot, fi, nil
		}

		if !os.IsNotExist(err) {
			return "", fi, err
		}
	}
}

// FindRepoRoot returns the absolute path to the root of the repository and git related dir locations:
// - wt: if "" => the git checkout at root is not a worktree or path to git worktree folder
// - gitdir: absolute path to root of git repository dir ($root/.git if not in worktree)
func FindRepoRoot(path string) (root, gitdir, wt string, err error) {
	path = cmp.Or(path, ".")
	dot, fi, err := findDotGit(path)
	if err != nil {
		return "", "", "", err
	}
	root = filepath.Dir(dot)
	if fi.IsDir() {
		return root, dot, "", nil
	}
	gitdir, wt, err = findWorktreeOfDotGit(dot)
	if err != nil {
		return "", "", "", err
	}
	return root, gitdir, wt, nil
}

func FindLocalRepo(path string) (*LocalRepo, error) {
	root, gitdir, wt, err := FindRepoRoot(path)
	if err != nil {
		return nil, fmt.Errorf("failed to detect repo root at %s: %w", path, err)
	}
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, fmt.Errorf("not a git repository at %s: %w", root, err)
	}
	return &LocalRepo{
		Root:     root,
		GitDir:   gitdir,
		Worktree: wt,
		GitRepo:  repo,
	}, nil
}

func findWorktreeOfDotGit(dot string) (gitdir, wt string, err error) {
	b, err := os.ReadFile(dot)
	if err != nil {
		return "", "", err
	}

	line, _, _ := strings.Cut(string(b), "\n")
	const prefix = "gitdir: "
	wt, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return "", "", fmt.Errorf(".git file has no %s prefix", prefix)
	}

	wt = strings.TrimSpace(wt)

	b, err = os.ReadFile(filepath.Join(wt, "gitdir"))
	if err != nil {
		return "", "", err
	}

	gitdir, err = filepath.Abs(strings.TrimSpace(string(b)))
	if err != nil {
		return "", "", err
	}

	return gitdir, wt, nil
}

// parseRemoteURL handles HTTPS/SSH URLs and scp-like syntax and returns host, port, owner, repo
func parseRemoteURL(raw string) (host string, port int, owner string, repo string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, "", ""
	}

	// Try standard URL parsing first
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "ssh://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host = u.Hostname()
			if p := u.Port(); p != "" {
				if n, err := strconv.Atoi(p); err == nil {
					port = n
				}
			}
			// Path: /owner/repo(.git)
			pth := strings.Trim(u.Path, "/")
			owner, repo = splitOwnerRepo(pth)
			return hostOnly(host), port, owner, trimDotGit(repo)
		}
	}

	// Handle scp-like syntax: git@host:owner/repo.git
	scp := regexp.MustCompile(`^(?:(?P<user>[^@]+)@)?(?P<host>[^:]+):(?P<path>.+)$`)
	if m := scp.FindStringSubmatch(raw); len(m) == 4 {
		host = m[2]
		pth := m[3]
		owner, repo = splitOwnerRepo(pth)
		return hostOnly(host), 0, owner, trimDotGit(repo)
	}

	// Fallback: maybe it's just host/owner/repo
	parts := strings.Split(strings.TrimPrefix(raw, "https://"), "/")
	if len(parts) >= 3 {
		host = parts[0]
		owner = parts[1]
		repo = parts[2]
	}
	return hostOnly(host), 0, owner, trimDotGit(repo)
}

func splitOwnerRepo(p string) (string, string) {
	p = strings.Trim(p, "/")
	// Support nested groups (gitlab): group/subgroup/repo
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return "", trimDotGit(p)
	}
	owner := strings.Join(segs[:len(segs)-1], "/")
	repo := segs[len(segs)-1]
	return owner, trimDotGit(repo)
}

func trimDotGit(s string) string {
	return strings.TrimSuffix(path.Base(s), ".git")
}

func hostOnly(h string) string {
	// Remove any port from host (e.g., example.com:2222)
	if before, _, ok := strings.Cut(h, ":"); ok {
		return before
	}
	return h
}

// HTTP probing and service detection based on the Python reference implementation

var httpClient = &http.Client{Timeout: 2500 * time.Millisecond}

func httpGet(url string) (code int, headers map[string]string, body []byte) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "git-remote-prober/0.1")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, map[string]string{}, nil
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	headers = map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}
	b, _ := io.ReadAll(resp.Body)
	return code, headers, b
}

// Provider represents a supported git hosting provider.
type Provider int

const (
	ProviderUnknown Provider = iota
	ProviderGitHub
	ProviderGitLab
	ProviderBitbucket
)

func (p Provider) String() string {
	switch p {
	case ProviderGitHub:
		return "github"
	case ProviderGitLab:
		return "gitlab"
	case ProviderBitbucket:
		return "bitbucket"
	default:
		return "unknown"
	}
}

func detectService(host string, port int) (service Provider, variant, evidence, httpBase string) {
	if host == "" {
		return ProviderUnknown, "unknown", "none", ""
	}

	// Known cloud hosts
	switch strings.ToLower(host) {
	case "github.com":
		return ProviderGitHub, "cloud", "url-heuristic", ""
	case "gitlab.com":
		return ProviderGitLab, "cloud", "url-heuristic", ""
	case "bitbucket.org":
		return ProviderBitbucket, "cloud", "url-heuristic", ""
	}

	hostsToTry := []string{}
	if port > 0 {
		hostsToTry = append(hostsToTry, fmt.Sprintf("https://%s:%d", host, port))
		hostsToTry = append(hostsToTry, fmt.Sprintf("http://%s:%d", host, port))
	} else {
		hostsToTry = append(hostsToTry, fmt.Sprintf("https://%s", host))
		hostsToTry = append(hostsToTry, fmt.Sprintf("http://%s", host))
	}

	for _, base := range hostsToTry {
		if ok, ev := probeGitLab(base); ok {
			return ProviderGitLab, "self-hosted", ev, base
		}
		if ok, ev := probeGitHubEnterprise(base); ok {
			return ProviderGitHub, "self-hosted", ev, base
		}
		if ok, ev := probeBitbucketServer(base); ok {
			return ProviderBitbucket, "self-hosted", ev, base
		}
	}

	return ProviderUnknown, "unknown", "probe-failed", ""
}

func probeGitLab(base string) (bool, string) {
	// /api/v4/version preferred
	code, headers, body := httpGet(base + "/api/v4/version")
	if code == 200 && len(body) > 0 {
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			if _, ok := data["version"]; ok {
				return true, "api_v4_version"
			}
		}
	}
	if code == 200 || code == 401 || code == 403 || code == 302 {
		hints := []string{
			"x-gitlab-meta", "gitlab-lb", "gitlab-sv", "gitlab-workhorse",
			"gitlab-instance-id", "x-gitlab-feature-flags", "x-gitlab-rate-limit-remaining", "x-gitlab-rate-limit-limit",
		}
		for _, k := range hints {
			if _, ok := headers[k]; ok {
				return true, "headers"
			}
		}
		server := strings.ToLower(headers["server"])
		wwwAuth := strings.ToLower(headers["www-authenticate"])
		if strings.Contains(server, "gitlab") || strings.Contains(wwwAuth, "gitlab") {
			return true, "headers"
		}
	}
	return false, ""
}

func probeGitHubEnterprise(base string) (bool, string) {
	code, headers, body := httpGet(base + "/api/v3/meta")
	gheVer := headers["x-github-enterprise-version"]
	if code == 200 && len(body) > 0 {
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			if gheVer != "" || hasKey(data, "verifiable_password_authentication") {
				return true, "api_v3_meta"
			}
		}
	}
	code, headers, body = httpGet(base + "/api/v3")
	gheVer = headers["x-github-enterprise-version"]
	if code == 200 && len(body) > 0 {
		var data map[string]any
		if json.Unmarshal(body, &data) == nil {
			if gheVer != "" || hasKey(data, "current_user_url") {
				return true, "api_v3"
			}
		}
	}
	return false, ""
}

func probeBitbucketServer(base string) (bool, string) {
	paths := []string{"/rest/api/latest/application-properties", "/rest/api/1.0/application-properties"}
	for _, p := range paths {
		code, headers, body := httpGet(base + p)
		if code == 200 && len(body) > 0 {
			var data map[string]any
			if json.Unmarshal(body, &data) == nil {
				if _, ok := data["version"]; ok {
					return true, "application-properties"
				}
				if dn, _ := data["displayName"].(string); strings.Contains(strings.ToLower(dn), "bitbucket") {
					return true, "application-properties"
				}
			}
		}
		if code == 401 {
			// Atlassian header hints
			if hasAnyHeader(headers, []string{"x-arequestid", "x-asessionid", "x-seraph-loginreason"}) {
				return true, "atlassian-headers"
			}
		}
	}
	return false, ""
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }
func hasAnyHeader(h map[string]string, keys []string) bool {
	for _, k := range keys {
		if _, ok := h[strings.ToLower(k)]; ok {
			return true
		}
	}
	return false
}

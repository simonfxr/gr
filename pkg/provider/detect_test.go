package provider

import (
	"net/http"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/simonfxr/gr/pkg/config"
)

// detectRemote runs provider detection for a repository whose origin is remote.
func detectRemote(t *testing.T, remote string, cfg *config.Config) (*Info, error) {
	t.Helper()
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatal(err)
	}
	return DetectFromRepo(&LocalRepo{GitRepo: repo}, cfg)
}

func TestDetectConfiguredHost(t *testing.T) {
	// .invalid never resolves: probing would fail the expectations below.
	cfg := &config.Config{Hosts: map[string]config.HostConfig{
		"git.example.invalid": {Provider: "GitLab", APIURL: "https://git.example.invalid/gitlab/api/v4/"},
	}}
	for _, remote := range []string{
		"https://git.example.invalid/gitlab/group/sub/proj.git",
		"git@git.example.invalid:group/sub/proj.git",
		"ssh://git@git.example.invalid:2222/group/sub/proj.git",
	} {
		info, err := detectRemote(t, remote, cfg)
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		if info.Provider != ProviderGitLab || info.Variant != "self-hosted" || info.Evidence != "config" ||
			info.APIBase != "https://git.example.invalid/gitlab/api/v4" || info.HTTPBase != "https://git.example.invalid/gitlab" ||
			info.Owner != "group/sub" || info.Repo != "proj" {
			t.Errorf("%s: info = %+v", remote, info)
		}
	}
}

func TestDetectSSHAliasOfCloudHost(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "env-token")
	cfg := &config.Config{Hosts: map[string]config.HostConfig{
		"gh-work": {Provider: "github", WebURL: "https://github.com", ProviderConfig: config.ProviderConfig{Token: "work-token"}},
	}}
	info, err := detectRemote(t, "git@gh-work:owner/repo.git", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != ProviderGitHub || info.Variant != "cloud" || info.APIBase != "" || info.Owner != "owner" {
		t.Errorf("info = %+v", info)
	}
	// The host's own token wins over GITHUB_TOKEN.
	token, err := resolveToken(info, func(c *config.Config) *config.ProviderConfig { return &c.GitHub }, "GITHUB_TOKEN")
	if err != nil || token != "work-token" {
		t.Errorf("token = %q, %v", token, err)
	}
}

func TestDetectCredentialsOnlyHostEntry(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "env-token")
	cfg := &config.Config{Hosts: map[string]config.HostConfig{
		"gitlab.com": {ProviderConfig: config.ProviderConfig{Token: "host-token"}},
	}}
	info, err := detectRemote(t, "git@gitlab.com:group/proj.git", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != ProviderGitLab || info.Variant != "cloud" || info.HostConfig == nil {
		t.Errorf("info = %+v", info)
	}
	if token, _ := resolveToken(info, func(c *config.Config) *config.ProviderConfig { return &c.GitLab }, "GITLAB_TOKEN"); token != "host-token" {
		t.Errorf("token = %q", token)
	}
}

func TestDetectRejectsBadHostConfig(t *testing.T) {
	for _, hc := range []config.HostConfig{
		{Provider: "gitea"},
		{Provider: "gitlab", APIURL: "git.example.invalid/api/v4"},
		{Provider: "gitlab", WebURL: "ftp://git.example.invalid"},
	} {
		cfg := &config.Config{Hosts: map[string]config.HostConfig{"git.example.invalid": hc}}
		if _, err := detectRemote(t, "git@git.example.invalid:group/proj.git", cfg); err == nil {
			t.Errorf("%+v: expected error", hc)
		}
	}
}

func TestRefuseTokenOverProbedPlainHTTP(t *testing.T) {
	f, info := fakeGitLab(t)
	info.Evidence = "api_v4_version" // found by probing, not configured
	_, err := ProviderGitLab.PrView(t.Context(), info, 5)
	if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) > 0 {
		t.Errorf("sent %v", f.calls)
	}
}

func TestConfiguredAPIURL(t *testing.T) {
	f, info := fakeGitLab(t)
	// Instance below a path prefix; the web root alone would be wrong.
	info.HTTPBase = "https://wrong.invalid"
	info.APIBase = f.srv.URL + "/prefix/api/v4"
	f.reply("GET /prefix/api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, gitlabMR5)
	if _, err := ProviderGitLab.PrView(t.Context(), info, 5); err != nil {
		t.Fatal(err)
	}
}

func TestIsSSHRemote(t *testing.T) {
	for remote, want := range map[string]bool{
		"git@host:o/r.git":         true,
		"ssh://git@host:22/o/r":    true,
		"git+ssh://host/o/r":       true,
		"https://host/o/r.git":     false,
		"http://host:8080/o/r.git": false,
	} {
		if got := isSSHRemote(remote); got != want {
			t.Errorf("isSSHRemote(%q) = %v", remote, got)
		}
	}
}

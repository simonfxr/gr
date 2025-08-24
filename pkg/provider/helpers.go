package provider

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	git "github.com/go-git/go-git/v5"
	githublib "github.com/google/go-github/v74/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

// githubClient returns an authenticated GitHub client.
// Supports both github.com and GitHub Enterprise when Info indicates self-hosted.
func githubClient(ctx context.Context, nfo *Info) (*githublib.Client, error) {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN not set")
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)

	if nfo != nil && nfo.Provider == ProviderGitHub && nfo.Variant == "self-hosted" && nfo.HTTPBase != "" {
		base := strings.TrimRight(nfo.HTTPBase, "/") + "/api/v3/"
		// For our usage, the REST and upload URLs are the same base.
		return githublib.NewEnterpriseClient(base, base, tc)
	}
	return githublib.NewClient(tc), nil
}

// gitlabClient returns an authenticated GitLab client, respecting self-hosted base URL when available.
func gitlabClient(nfo *Info) (*gitlab.Client, error) {
	token := strings.TrimSpace(os.Getenv("GITLAB_TOKEN"))
	if token == "" {
		return nil, errors.New("GITLAB_TOKEN not set")
	}
	if nfo != nil && nfo.Provider == ProviderGitLab && nfo.Variant == "self-hosted" && nfo.HTTPBase != "" {
		return gitlab.NewClient(token, gitlab.WithBaseURL(strings.TrimRight(nfo.HTTPBase, "/")+"/api/v4"))
	}
	return gitlab.NewClient(token)
}

// httpClientWithToken is exposed for future use or tests if needed.
func httpClientWithToken(token string) *http.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return oauth2.NewClient(context.Background(), ts)
}

// bitbucketClient returns a Bitbucket Cloud client. For now Bitbucket Server is not supported.
// Auth precedence:
// - If BITBUCKET_USERNAME and BITBUCKET_TOKEN are set -> Basic auth (app password)
// - Else if BITBUCKET_TOKEN is set -> OAuth bearer token
func bitbucketClient(nfo *Info) (*bitbucket.Client, error) {
	if nfo != nil && nfo.Provider == ProviderBitbucket && nfo.Variant == "self-hosted" {
		return nil, errors.New("bitbucket server is not supported yet")
	}
	token := strings.TrimSpace(os.Getenv("BITBUCKET_TOKEN"))
	user := strings.TrimSpace(os.Getenv("BITBUCKET_USERNAME"))
	if user != "" && token != "" {
		return bitbucket.NewBasicAuth(user, token), nil
	}
	if token != "" {
		return bitbucket.NewOAuthbearerToken(token), nil
	}
	return nil, errors.New("BITBUCKET_TOKEN not set (or provide BITBUCKET_USERNAME + BITBUCKET_TOKEN)")
}

// CurrentBranch returns the current branch name of the repository detected from Info.
func CurrentBranch(nfo *Info) (string, error) {
	root, err := FindRepoRoot("")
	if err != nil {
		return "", err
	}
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Name().Short(), nil
}

// DefaultBranchGitHub returns the default branch for a GitHub repository.
func DefaultBranchGitHub(ctx context.Context, gh *githublib.Client, nfo *Info) (string, error) {
	repo, _, err := gh.Repositories.Get(ctx, nfo.Owner, nfo.Repo)
	if err != nil {
		return "", err
	}
	b := repo.GetDefaultBranch()
	if b == "" {
		return "main", nil
	}
	return b, nil
}

// DefaultBranchGitLab returns the default branch for a GitLab project.
func DefaultBranchGitLab(ctx context.Context, gl *gitlab.Client, nfo *Info) (string, error) {
	projectPath := nfo.Owner + "/" + nfo.Repo
	proj, _, err := gl.Projects.GetProject(projectPath, nil)
	if err != nil {
		return "", err
	}
	if proj.DefaultBranch != "" {
		return proj.DefaultBranch, nil
	}
	return "main", nil
}

// DefaultBranchBitbucket returns the main branch (default) for a Bitbucket Cloud repository.
func DefaultBranchBitbucket(ctx context.Context, bb *bitbucket.Client, nfo *Info) (string, error) {
	_ = ctx
	repo, err := bb.Repositories.Repository.Get(&bitbucket.RepositoryOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
	})
	if err != nil {
		return "", err
	}
	if repo != nil && repo.Mainbranch.Name != "" {
		return repo.Mainbranch.Name, nil
	}
	return "main", nil
}

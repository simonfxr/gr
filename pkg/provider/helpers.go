package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"

	"github.com/simonfxr/gr/pkg/auth"
	"github.com/simonfxr/gr/pkg/config"
)

// githubClient returns a GitHub client using token from config/env.
func githubClient(ctx context.Context, nfo *Info) (*githublib.Client, error) {
	token, err := resolveToken(nfo, func(c *config.Config) *config.ProviderConfig { return &c.GitHub }, "GITHUB_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("github token: %w", err)
	}
	if token == "" {
		// Anonymous access would hide private repositories behind 404s and
		// hit the 60 requests/hour limit quickly.
		return nil, missingToken("github", "GITHUB_TOKEN", "github")
	}
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	if nfo != nil && (nfo.APIBase != "" || nfo.Variant == "self-hosted") {
		base, err := apiBase(nfo, "/api/v3")
		if err != nil {
			return nil, err
		}
		base += "/"
		return githublib.NewEnterpriseClient(base, base, httpClient)
	}
	return githublib.NewClient(httpClient), nil
}

// gitlabClient returns an authenticated GitLab client using token from
// config/env. All its requests use ctx.
func gitlabClient(ctx context.Context, nfo *Info) (*gitlab.Client, error) {
	token, err := resolveToken(nfo, func(c *config.Config) *config.ProviderConfig { return &c.GitLab }, "GITLAB_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("gitlab token: %w", err)
	}
	if token == "" {
		return nil, missingToken("gitlab", "GITLAB_TOKEN", "gitlab")
	}
	opts := []gitlab.ClientOptionFunc{gitlab.WithRequestOptions(gitlab.WithContext(ctx))}
	if nfo != nil && (nfo.APIBase != "" || nfo.Variant == "self-hosted") {
		base, err := apiBase(nfo, "/api/v4")
		if err != nil {
			return nil, err
		}
		opts = append(opts, gitlab.WithBaseURL(base))
	}
	return gitlab.NewClient(token, opts...)
}

// bitbucketCredentials returns the Bitbucket Cloud username (may be empty) and token.
func bitbucketCredentials(nfo *Info) (string, string, error) {
	if nfo != nil && nfo.Provider == ProviderBitbucket && nfo.Variant == "self-hosted" {
		return "", "", fmt.Errorf("bitbucket server is not supported yet (if %s is an SSH alias of bitbucket.org, set web_url = \"https://bitbucket.org\" under [hosts.%q])", nfo.Host, nfo.Host)
	}
	section := func(c *config.Config) *config.ProviderConfig { return &c.Bitbucket }
	token, err := resolveToken(nfo, section, "BITBUCKET_TOKEN")
	if err != nil {
		return "", "", fmt.Errorf("bitbucket token: %w", err)
	}
	if token == "" {
		return "", "", missingToken("bitbucket", "BITBUCKET_TOKEN", "bitbucket")
	}
	var user string
	if hc := hostCredentials(nfo); hc != nil {
		user = auth.GetUsername(hc, "")
	}
	if user == "" {
		user = auth.GetUsername(providerSection(nfo, section), "BITBUCKET_USERNAME")
	}
	return user, token, nil
}

// resolveToken returns the token for nfo's host: its [hosts] credentials,
// else the env variable, else the provider section of the config.
func resolveToken(nfo *Info, section func(*config.Config) *config.ProviderConfig, env string) (string, error) {
	if hc := hostCredentials(nfo); hc != nil {
		if token, err := auth.GetToken(hc, ""); err != nil || token != "" {
			return token, err
		}
	}
	return auth.GetToken(providerSection(nfo, section), env)
}

func hostCredentials(nfo *Info) *config.ProviderConfig {
	if nfo == nil || nfo.HostConfig == nil {
		return nil
	}
	return &nfo.HostConfig.ProviderConfig
}

func providerSection(nfo *Info, section func(*config.Config) *config.ProviderConfig) *config.ProviderConfig {
	if nfo == nil || nfo.Config == nil {
		return nil
	}
	return section(nfo.Config)
}

// apiBase returns the API root of a self-hosted instance: the configured
// api_url, or path below the web root. A plain-http root that was only
// found by probing is refused, as the token would be sent in cleartext;
// configuring the host explicitly opts in.
func apiBase(nfo *Info, path string) (string, error) {
	if nfo.APIBase != "" {
		return nfo.APIBase, nil
	}
	base := strings.TrimRight(nfo.HTTPBase, "/")
	if base == "" {
		return "", fmt.Errorf("no API URL known for %s; set api_url under [hosts.%q]", nfo.Host, nfo.Host)
	}
	if strings.HasPrefix(base, "http://") && nfo.Evidence != "config" {
		return "", fmt.Errorf("%s answered only over plain HTTP; refusing to send credentials unencrypted (to allow it, set web_url = %q under [hosts.%q])", nfo.Host, base, nfo.Host)
	}
	return base + path, nil
}

// CurrentBranch returns the current branch name of the repository detected from Info.
func CurrentBranch(nfo *Info) (string, error) {
	repo := nfo.GitRepo
	if repo == nil {
		return "", fmt.Errorf("local repo not available")
	}

	head, err := repo.Head()
	if err != nil {
		return "", err
	}

	branchName := head.Name().Short()
	return branchName, nil
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
	projectPath := gitlabProject(nfo)

	proj, _, err := gl.Projects.GetProject(projectPath, nil)
	if err != nil {
		return "", err
	}

	if proj.DefaultBranch != "" {
		return proj.DefaultBranch, nil
	}
	return "main", nil
}

// LastCommitTitle returns the first line of the last commit message on HEAD.
func LastCommitTitle(nfo *Info) (string, error) {
	repo := nfo.GitRepo
	if repo == nil {
		return "", fmt.Errorf("No local repo present")
	}
	ref, err := repo.Head()
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return "", err
	}
	// keep object import referenced
	_ = object.Commit{}
	msg := strings.TrimSpace(commit.Message)
	if msg == "" {
		return "", nil
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg), nil
}

// ResolvePRTitle ensures a non-empty title, using last commit subject when missing.
func ResolvePRTitle(inTitle string, nfo *Info) (string, error) {
	t := strings.TrimSpace(inTitle)
	if t != "" {
		return t, nil
	}
	last, err := LastCommitTitle(nfo)
	if err == nil && strings.TrimSpace(last) != "" {
		return last, nil
	}
	return "", errors.New("PR title is required; provide --title or make a commit")
}

// LocalBranchRename renames a local branch reference and updates HEAD if needed.
func LocalBranchRename(repo *git.Repository, oldName, newName string) error {
	if repo == nil {
		return fmt.Errorf("local repo not available")
	}
	st := repo.Storer
	oldRefName := plumbing.NewBranchReferenceName(strings.TrimSpace(oldName))
	newRefName := plumbing.NewBranchReferenceName(strings.TrimSpace(newName))
	oldRef, err := repo.Reference(oldRefName, true)
	if err != nil {
		return err
	}
	// Create new ref at same hash
	newRef := plumbing.NewHashReference(newRefName, oldRef.Hash())
	if err := st.SetReference(newRef); err != nil {
		return err
	}
	// Remove old ref
	if err := st.RemoveReference(oldRefName); err != nil {
		return err
	}
	// If HEAD symbolically points to old, update to new
	if headRef, err := st.Reference(plumbing.HEAD); err == nil && headRef.Type() == plumbing.SymbolicReference {
		if headRef.Target() == oldRefName {
			if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, newRefName)); err != nil {
				return err
			}
		}
	}
	return nil
}

// LocalBranchDelete deletes a local branch reference. It refuses to delete the
// currently checked out branch. The force flag is currently ignored for safety
// (git also refuses deletion of the current branch).
func LocalBranchDelete(repo *git.Repository, name string, force bool) error {
	if repo == nil {
		return fmt.Errorf("local repo not available")
	}
	st := repo.Storer
	// Determine current branch
	headRef, err := repo.Head()
	if err != nil {
		return err
	}
	cur := headRef.Name().Short()
	target := strings.TrimSpace(name)
	if target == "" {
		return errors.New("branch name is required")
	}
	if cur == target {
		return errors.New("cannot delete the current branch")
	}
	// Remove reference if exists
	refName := plumbing.NewBranchReferenceName(target)
	if _, err := repo.Reference(refName, true); err != nil {
		return err
	}
	if err := st.RemoveReference(refName); err != nil {
		return err
	}
	return nil
}

package provider

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

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
	var cfg *config.ProviderConfig
	if nfo != nil && nfo.Config != nil {
		cfg = &nfo.Config.GitHub
	}
	token, err := auth.GetToken(cfg, "GITHUB_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("github token: %w", err)
	}
	if token == "" {
		// Anonymous access would hide private repositories behind 404s and
		// hit the 60 requests/hour limit quickly.
		return nil, missingToken("github", "GITHUB_TOKEN", "github")
	}
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))

	if nfo != nil && nfo.Provider == ProviderGitHub && nfo.Variant == "self-hosted" && nfo.HTTPBase != "" {
		base := strings.TrimRight(nfo.HTTPBase, "/") + "/api/v3/"
		return githublib.NewEnterpriseClient(base, base, httpClient)
	}
	return githublib.NewClient(httpClient), nil
}

// gitlabClient returns an authenticated GitLab client using token from
// config/env. All its requests use ctx.
func gitlabClient(ctx context.Context, nfo *Info) (*gitlab.Client, error) {
	var cfg *config.ProviderConfig
	if nfo != nil && nfo.Config != nil {
		cfg = &nfo.Config.GitLab
	}
	token, err := auth.GetToken(cfg, "GITLAB_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("gitlab token: %w", err)
	}
	if token == "" {
		return nil, missingToken("gitlab", "GITLAB_TOKEN", "gitlab")
	}
	opts := []gitlab.ClientOptionFunc{gitlab.WithRequestOptions(gitlab.WithContext(ctx))}
	if nfo != nil && nfo.Provider == ProviderGitLab && nfo.Variant == "self-hosted" && nfo.HTTPBase != "" {
		opts = append(opts, gitlab.WithBaseURL(strings.TrimRight(nfo.HTTPBase, "/")+"/api/v4"))
	}
	return gitlab.NewClient(token, opts...)
}

// bitbucketCredentials returns the Bitbucket Cloud username (may be empty) and token.
func bitbucketCredentials(nfo *Info) (string, string, error) {
	if nfo != nil && nfo.Provider == ProviderBitbucket && nfo.Variant == "self-hosted" {
		return "", "", errors.New("bitbucket server is not supported yet")
	}
	var cfg *config.ProviderConfig
	if nfo != nil && nfo.Config != nil {
		cfg = &nfo.Config.Bitbucket
	}
	token, err := auth.GetToken(cfg, "BITBUCKET_TOKEN")
	if err != nil {
		return "", "", fmt.Errorf("bitbucket token: %w", err)
	}
	user := auth.GetUsername(cfg, "BITBUCKET_USERNAME")
	if token != "" {
		return user, token, nil
	}
	return "", "", missingToken("bitbucket", "BITBUCKET_TOKEN", "bitbucket")
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

func ParallelMap[T, U any](iter iter.Seq[T], f func(T) (U, error)) ([]U, error) {
	const npar = 32
	work := make(chan T)
	go func() {
		defer close(work)
		for x := range iter {
			work <- x
		}
	}()

	type result struct {
		u   U
		err error
	}
	wg := sync.WaitGroup{}
	results := make(chan result)

	go func() {
		wg.Wait()
		close(results)
	}()

	firstErr := atomic.Pointer[error]{}

	for range npar {
		wg.Go(func() {
			for x := range work {
				if firstErr.Load() != nil {
					continue
				}
				u, err := f(x)
				results <- result{u, err}
			}
		})
	}

	us := []U(nil)
	for res := range results {
		if firstErr.Load() != nil {
			continue
		}
		if err := res.err; err != nil {
			firstErr.Store(&err)
			continue
		}
		us = append(us, res.u)
	}

	if err := firstErr.Load(); err != nil {
		return nil, *err
	}

	return us, nil
}

func ParallelMapValues[T, U any](elems []T, f func(T) (U, error)) ([]U, error) {
	return ParallelMap(slices.Values(elems), f)
}

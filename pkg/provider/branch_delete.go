package provider

import (
	"context"
	"errors"
	"fmt"

	githublib "github.com/google/go-github/v74/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
)

type BranchDeleteOptions struct {
	Force bool
}

// BranchDelete deletes a remote branch across providers. It prevents deleting the
// default branch unless Force is true (where detectable).
func (p Provider) BranchDelete(ctx context.Context, nfo *Info, name string, opts BranchDeleteOptions) error {
	if nfo == nil {
		return errors.New("missing repo info")
	}
	if name == "" {
		return errors.New("branch name is required")
	}
	// Guard: do not delete default branch unless forced
	if !opts.Force {
		switch p {
		case ProviderGitHub:
			gh, err := githubClient(ctx, nfo)
			if err != nil {
				return err
			}
			def, err := DefaultBranchGitHub(ctx, gh, nfo)
			if err == nil && def == name {
				return fmt.Errorf("refusing to delete default branch %q (use --force)", name)
			}
		case ProviderGitLab:
			gl, err := gitlabClient(nfo)
			if err != nil {
				return err
			}
			def, err := DefaultBranchGitLab(ctx, gl, nfo)
			if err == nil && def == name {
				return fmt.Errorf("refusing to delete default branch %q (use --force)", name)
			}
		case ProviderBitbucket:
			bb, err := bitbucketClient(nfo)
			if err != nil {
				return err
			}
			def, err := DefaultBranchBitbucket(ctx, bb, nfo)
			if err == nil && def == name {
				return fmt.Errorf("refusing to delete default branch %q (use --force)", name)
			}
		}
	}

	switch p {
	case ProviderGitHub:
		return branchDeleteGitHub(ctx, nfo, name)
	case ProviderGitLab:
		return branchDeleteGitLab(ctx, nfo, name)
	case ProviderBitbucket:
		return branchDeleteBitbucket(ctx, nfo, name)
	default:
		return fmt.Errorf("unknown provider: %v", p)
	}
}

func branchDeleteGitHub(ctx context.Context, nfo *Info, name string) error {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return err
	}
	ref := fmt.Sprintf("refs/heads/%s", name)
	_, err = gh.Git.DeleteRef(ctx, nfo.Owner, nfo.Repo, ref)
	// go-github DeleteRef returns (*Response, error), ignore response
	if err != nil {
		// If branch does not exist, return a clearer message
		if ge, ok := err.(*githublib.ErrorResponse); ok && ge.Response != nil && ge.Response.StatusCode == 422 {
			return fmt.Errorf("branch %q not found or cannot be deleted", name)
		}
		return err
	}
	return nil
}

func branchDeleteGitLab(ctx context.Context, nfo *Info, name string) error {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
	_, err = gl.Branches.DeleteBranch(project, name)
	return err
}

func branchDeleteBitbucket(ctx context.Context, nfo *Info, name string) error {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return err
	}
	return bb.Repositories.Repository.DeleteBranch(&bitbucket.RepositoryBranchDeleteOptions{Owner: nfo.Owner, RepoSlug: nfo.Repo, RefName: name})
}

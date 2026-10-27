package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// CloseOptions controls PrClose.
type CloseOptions struct {
	// DeleteBranch deletes the source branch after closing, in the
	// repository it lives in, which may be a fork.
	DeleteBranch bool
}

// PrClose closes a pull/merge request without merging. If closing succeeded
// but deleting the source branch failed, PrClose returns the closed PR
// together with an error.
func (p Provider) PrClose(ctx context.Context, nfo *Info, number int, opts CloseOptions) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prCloseGitHub(ctx, nfo, number, opts)
	case ProviderGitLab:
		return prCloseGitLab(ctx, nfo, number, opts)
	case ProviderBitbucket:
		return prCloseBitbucket(ctx, nfo, number, opts)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prCloseGitHub(ctx context.Context, nfo *Info, number int, opts CloseOptions) (*PRDetails, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	state := "closed"
	pr, _, err := gh.PullRequests.Edit(ctx, nfo.Owner, nfo.Repo, number, &githublib.PullRequest{State: &state})
	if err != nil {
		return nil, err
	}
	d := githubPRDetails(pr)
	if opts.DeleteBranch {
		if err := githubDeleteHeadBranch(ctx, gh, pr); err != nil {
			return d, fmt.Errorf("PR #%d closed, but %w", number, err)
		}
	}
	return d, nil
}

func prCloseGitLab(ctx context.Context, nfo *Info, number int, opts CloseOptions) (*PRDetails, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	project := gitlabProject(nfo)
	close := "close"
	mr, _, err := gl.MergeRequests.UpdateMergeRequest(project, int64(number), &gitlab.UpdateMergeRequestOptions{
		StateEvent: &close,
	})
	if err != nil {
		return nil, err
	}
	d := gitlabPRDetails(&mr.BasicMergeRequest)
	if opts.DeleteBranch {
		// The source branch may live in a fork. GitLab itself refuses to
		// delete a project's default branch.
		var source any = project
		if mr.SourceProjectID != 0 {
			source = mr.SourceProjectID
		}
		if _, err := gl.Branches.DeleteBranch(source, mr.SourceBranch); err != nil {
			return d, fmt.Errorf("PR #%d closed, but deleting branch %q failed: %w", number, mr.SourceBranch, err)
		}
	}
	return d, nil
}

func prCloseBitbucket(ctx context.Context, nfo *Info, number int, opts CloseOptions) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	var pr bitbucketPR
	if _, err := bb.call(ctx, http.MethodPost, bitbucketPREndpoint(nfo, number)+"/decline", nil, &pr); err != nil {
		return nil, err
	}
	d := pr.toPRDetails()
	if opts.DeleteBranch {
		// Delete the branch in the source repository, which may be a fork.
		source := *nfo
		if owner, repo, ok := strings.Cut(pr.Source.Repository.FullName, "/"); ok {
			source.Owner, source.Repo = owner, repo
		}
		if err := ProviderBitbucket.BranchDelete(ctx, &source, d.Head, BranchDeleteOptions{}); err != nil {
			return d, fmt.Errorf("PR #%d closed, but deleting branch %q failed: %w", number, d.Head, err)
		}
	}
	return d, nil
}

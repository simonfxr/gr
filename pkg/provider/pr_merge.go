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

// MergeOptions controls how a PR/MR is merged.
type MergeOptions struct {
	Method       string // merge|squash|rebase (provider support varies)
	DeleteBranch bool
}

// PrMerge merges a pull/merge request across supported providers. If the
// merge succeeded but deleting the source branch failed, PrMerge returns the
// merged PR together with an error.
func (p Provider) PrMerge(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prMergeGitHub(ctx, nfo, number, opts)
	case ProviderGitLab:
		return prMergeGitLab(ctx, nfo, number, opts)
	case ProviderBitbucket:
		return prMergeBitbucket(ctx, nfo, number, opts)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prMergeGitHub(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	// Fetch PR to get head/base info for possible branch deletion
	pr, _, err := gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
	if err != nil {
		return nil, err
	}
	method := strings.ToLower(strings.TrimSpace(opts.Method))
	switch method {
	case "merge", "squash", "rebase":
		// ok
	case "":
		method = ""
	default:
		// ignore invalid method; let server default
		method = ""
	}
	var mergeOpts *githublib.PullRequestOptions
	if method != "" {
		mergeOpts = &githublib.PullRequestOptions{MergeMethod: method}
	}
	_, _, err = gh.PullRequests.Merge(ctx, nfo.Owner, nfo.Repo, number, "", mergeOpts)
	if err != nil {
		return nil, err
	}
	d, err := prViewGitHub(ctx, nfo, number)
	if err != nil {
		return nil, err
	}
	if opts.DeleteBranch {
		// Unlike GitLab and Bitbucket, GitHub's merge API cannot delete the
		// head branch. Delete it in the repository it lives in (maybe a fork).
		head := pr.GetHead()
		owner, repo := head.GetRepo().GetOwner().GetLogin(), head.GetRepo().GetName()
		if owner == "" || repo == "" {
			return d, fmt.Errorf("PR #%d merged, but its head repository no longer exists; branch %q not deleted", number, head.GetRef())
		}
		if _, err := gh.Git.DeleteRef(ctx, owner, repo, "heads/"+head.GetRef()); err != nil {
			return d, fmt.Errorf("PR #%d merged, but deleting branch %q failed: %w", number, head.GetRef(), err)
		}
	}
	return d, nil
}

func prMergeGitLab(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	project := gitlabProject(nfo)
	squash := false
	if strings.EqualFold(strings.TrimSpace(opts.Method), "squash") {
		squash = true
	}
	_, _, err = gl.MergeRequests.AcceptMergeRequest(project, int64(number), &gitlab.AcceptMergeRequestOptions{
		Squash:                   new(squash),
		ShouldRemoveSourceBranch: new(opts.DeleteBranch),
	})
	if err != nil {
		return nil, err
	}
	// Return updated view
	return prViewGitLab(ctx, nfo, number)
}

func prMergeBitbucket(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"close_source_branch": opts.DeleteBranch}
	var pr bitbucketPR
	status, err := bb.call(ctx, http.MethodPost, bitbucketPREndpoint(nfo, number)+"/merge", payload, &pr)
	if err != nil {
		return nil, err
	}
	if status == http.StatusAccepted || pr.ID == 0 {
		// Queued or still merging in the background: report the current state.
		return prViewBitbucket(ctx, nfo, number)
	}
	return pr.toPRDetails(), nil
}

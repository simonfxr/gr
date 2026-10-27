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
	// Method is merge, squash or rebase; empty uses the provider's or
	// repository's default. GitLab does not support rebase.
	Method string
	// DeleteBranch deletes (true) or keeps (false) the source branch; nil
	// leaves it to the PR's or repository's setting.
	DeleteBranch *bool
}

// PrMerge merges a pull/merge request across supported providers. If the
// merge succeeded but deleting the source branch failed, PrMerge returns the
// merged PR together with an error.
func (p Provider) PrMerge(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	opts.Method = strings.ToLower(strings.TrimSpace(opts.Method))
	switch opts.Method {
	case "", "merge", "squash", "rebase":
	default:
		return nil, invalidArgf("invalid merge method %q (want merge, squash or rebase)", opts.Method)
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
	var mergeOpts *githublib.PullRequestOptions
	if opts.Method != "" {
		mergeOpts = &githublib.PullRequestOptions{MergeMethod: opts.Method}
	}
	_, _, err = gh.PullRequests.Merge(ctx, nfo.Owner, nfo.Repo, number, "", mergeOpts)
	if err != nil {
		return nil, err
	}
	d, err := prViewGitHub(ctx, nfo, number)
	if err != nil {
		return nil, err
	}
	if opts.DeleteBranch != nil && *opts.DeleteBranch {
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
	accept := &gitlab.AcceptMergeRequestOptions{ShouldRemoveSourceBranch: opts.DeleteBranch}
	switch opts.Method {
	case "merge":
		accept.Squash = new(false)
	case "squash":
		accept.Squash = new(true)
	case "rebase":
		// Fast-forward vs. merge commit is a project setting on GitLab.
		return nil, invalidArgf("GitLab does not support --method rebase; the project's merge method applies (use merge or squash)")
	}
	mr, _, err := gl.MergeRequests.AcceptMergeRequest(gitlabProject(nfo), int64(number), accept)
	if err != nil {
		return nil, err
	}
	return gitlabPRDetails(&mr.BasicMergeRequest), nil
}

// bitbucketMergeStrategies maps gr merge methods to Bitbucket merge strategies.
var bitbucketMergeStrategies = map[string]string{
	"merge":  "merge_commit",
	"squash": "squash",
	"rebase": "rebase_fast_forward",
}

func prMergeBitbucket(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if opts.DeleteBranch != nil {
		payload["close_source_branch"] = *opts.DeleteBranch
	}
	if opts.Method != "" {
		payload["merge_strategy"] = bitbucketMergeStrategies[opts.Method]
	}
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

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	githublib "github.com/google/go-github/v74/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// MergeOptions controls how a PR/MR is merged.
type MergeOptions struct {
	Method       string // merge|squash|rebase (provider support varies)
	DeleteBranch bool
}

// PrMerge merges a pull/merge request across supported providers.
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
	_, _, err = gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
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
	// Return updated view
	return prViewGitHub(ctx, nfo, number)
}

func prMergeGitLab(ctx context.Context, nfo *Info, number int, opts MergeOptions) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
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
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	// Perform merge
	po := &bitbucket.PullRequestsOptions{
		Owner:             nfo.Owner,
		RepoSlug:          nfo.Repo,
		ID:                fmt.Sprintf("%d", number),
		CloseSourceBranch: opts.DeleteBranch,
	}
	_, err = bb.Repositories.PullRequests.Merge(po)
	if err != nil {
		return nil, err
	}
	// Return updated view; Bitbucket library returns interface{} without strong type
	d, err := prViewBitbucket(ctx, nfo, number)
	if err == nil {
		// On success, mark as merged if not yet reflected
		if !d.Merged {
			d.Merged = true
			d.State = "merged"
			d.UpdatedAt = time.Now()
		}
		return d, nil
	}
	// Fallback minimal
	return &PRDetails{Number: number, Merged: true, State: "merged"}, nil
}

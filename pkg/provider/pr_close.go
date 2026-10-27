package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PrClose closes a pull/merge request without merging.
func (p Provider) PrClose(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prCloseGitHub(ctx, nfo, number)
	case ProviderGitLab:
		return prCloseGitLab(ctx, nfo, number)
	case ProviderBitbucket:
		return prCloseBitbucket(ctx, nfo, number)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prCloseGitHub(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	state := "closed"
	pr, _, err := gh.PullRequests.Edit(ctx, nfo.Owner, nfo.Repo, number, &githublib.PullRequest{State: &state})
	if err != nil {
		return nil, err
	}
	return githubPRDetails(pr), nil
}

func prCloseGitLab(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
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
	return gitlabPRDetails(&mr.BasicMergeRequest), nil
}

func prCloseBitbucket(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	var pr bitbucketPR
	if _, err := bb.call(ctx, http.MethodPost, bitbucketPREndpoint(nfo, number)+"/decline", nil, &pr); err != nil {
		return nil, err
	}
	return pr.toPRDetails(), nil
}

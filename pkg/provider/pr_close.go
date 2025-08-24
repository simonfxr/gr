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
	// Convert to PRDetails
	var created, updated time.Time
	if pr.CreatedAt != nil {
		created = pr.CreatedAt.Time
	}
	if pr.UpdatedAt != nil {
		updated = pr.UpdatedAt.Time
	}
	author := ""
	if pr.User != nil {
		author = pr.User.GetLogin()
	}
	base := ""
	if pr.Base != nil {
		base = pr.Base.GetRef()
	}
	head := ""
	if pr.Head != nil {
		head = pr.Head.GetRef()
	}
	merged := pr.MergedAt != nil && !pr.MergedAt.IsZero()
	return &PRDetails{
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		Body:      pr.GetBody(),
		Author:    author,
		State:     pr.GetState(),
		CreatedAt: created,
		UpdatedAt: updated,
		Merged:    merged,
		Draft:     pr.GetDraft(),
		Base:      base,
		Head:      head,
		URL:       pr.GetHTMLURL(),
	}, nil
}

func prCloseGitLab(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
	close := "close"
	mr, _, err := gl.MergeRequests.UpdateMergeRequest(project, number, &gitlab.UpdateMergeRequestOptions{
		StateEvent: &close,
	})
	if err != nil {
		return nil, err
	}
	var created, updated time.Time
	if mr.CreatedAt != nil {
		created = *mr.CreatedAt
	}
	if mr.UpdatedAt != nil {
		updated = *mr.UpdatedAt
	}
	author := ""
	if mr.Author != nil {
		author = mr.Author.Username
	}
	merged := mr.MergedAt != nil && !mr.MergedAt.IsZero()
	return &PRDetails{
		Number:    mr.IID,
		Title:     mr.Title,
		Body:      mr.Description,
		Author:    author,
		State:     mr.State,
		CreatedAt: created,
		UpdatedAt: updated,
		Merged:    merged,
		Draft:     false,
		Base:      mr.TargetBranch,
		Head:      mr.SourceBranch,
		URL:       mr.WebURL,
	}, nil
}

func prCloseBitbucket(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	po := &bitbucket.PullRequestsOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
		ID:       fmt.Sprintf("%d", number),
	}
	_, err = bb.Repositories.PullRequests.Decline(po)
	if err != nil {
		return nil, err
	}
	d, err := prViewBitbucket(ctx, nfo, number)
	if err == nil {
		if !strings.EqualFold(d.State, "declined") {
			d.State = "declined"
			d.UpdatedAt = time.Now()
		}
		return d, nil
	}
	return &PRDetails{Number: number, State: "declined"}, nil
}

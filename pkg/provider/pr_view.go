package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PRDetails is a provider-agnostic view of a single PR/MR.
type PRDetails struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Merged    bool      `json:"merged"`
	Draft     bool      `json:"draft"`
	Base      string    `json:"base"`
	Head      string    `json:"head"`
	URL       string    `json:"url"`
}

// PrView retrieves details for a single pull/merge request.
func (p Provider) PrView(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prViewGitHub(ctx, nfo, number)
	case ProviderGitLab:
		return prViewGitLab(ctx, nfo, number)
	case ProviderBitbucket:
		return prViewBitbucket(ctx, nfo, number)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prViewGitHub(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	pr, _, err := gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
	if err != nil {
		return nil, err
	}
	return githubPRDetails(pr), nil
}

func prViewGitLab(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	mr, _, err := gl.MergeRequests.GetMergeRequest(gitlabProject(nfo), int64(number), nil)
	if err != nil {
		return nil, err
	}
	return gitlabPRDetails(&mr.BasicMergeRequest), nil
}

// githubPRDetails converts a GitHub pull request; getters are nil-safe.
func githubPRDetails(pr *githublib.PullRequest) *PRDetails {
	return &PRDetails{
		Number:    pr.GetNumber(),
		Title:     pr.GetTitle(),
		Body:      pr.GetBody(),
		Author:    pr.GetUser().GetLogin(),
		State:     pr.GetState(),
		CreatedAt: pr.GetCreatedAt().Time,
		UpdatedAt: pr.GetUpdatedAt().Time,
		Merged:    !pr.GetMergedAt().IsZero(),
		Draft:     pr.GetDraft(),
		Base:      pr.GetBase().GetRef(),
		Head:      pr.GetHead().GetRef(),
		URL:       pr.GetHTMLURL(),
	}
}

// gitlabPRDetails converts a GitLab merge request.
func gitlabPRDetails(mr *gitlab.BasicMergeRequest) *PRDetails {
	d := &PRDetails{
		Number: int(mr.IID),
		Title:  mr.Title,
		Body:   mr.Description,
		State:  mr.State,
		Merged: mr.MergedAt != nil && !mr.MergedAt.IsZero(),
		Draft:  false, // GitLab draft/WIP not standardized in this client; omit
		Base:   mr.TargetBranch,
		Head:   mr.SourceBranch,
		URL:    mr.WebURL,
	}
	if mr.Author != nil {
		d.Author = mr.Author.Username
	}
	if mr.CreatedAt != nil {
		d.CreatedAt = *mr.CreatedAt
	}
	if mr.UpdatedAt != nil {
		d.UpdatedAt = *mr.UpdatedAt
	}
	return d
}

// gitlabProject is the project path ("group/sub/repo") used as GitLab project ID.
func gitlabProject(nfo *Info) string {
	return nfo.Owner + "/" + nfo.Repo
}

// Summary returns the list representation of a PR.
func (d *PRDetails) Summary() PullRequest {
	return PullRequest{Number: d.Number, Title: d.Title, Author: d.Author, State: d.State, CreatedAt: d.CreatedAt, URL: d.URL}
}

func prViewBitbucket(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	pr, err := bitbucketGetPR(ctx, bb, nfo, number)
	if err != nil {
		return nil, err
	}
	return pr.toPRDetails(), nil
}

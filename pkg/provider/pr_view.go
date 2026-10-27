package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PR states, the same for all providers.
const (
	StateOpen   = "open"
	StateClosed = "closed" // closed without merging
	StateMerged = "merged"
)

// PRDetails is a provider-agnostic view of a single PR/MR.
type PRDetails struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Author string `json:"author"`
	// State is StateOpen, StateClosed or StateMerged.
	State string `json:"state"`
	// ProviderState is the provider's own state, lowercased, e.g. GitLab
	// "opened"/"locked" or Bitbucket "declined"/"superseded".
	ProviderState string    `json:"provider_state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Merged        bool      `json:"merged"`
	Draft         bool      `json:"draft"`
	Base          string    `json:"base"`
	Head          string    `json:"head"`
	URL           string    `json:"url"`
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
	gl, err := gitlabClient(ctx, nfo)
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
		Number:        pr.GetNumber(),
		Title:         pr.GetTitle(),
		Body:          pr.GetBody(),
		Author:        pr.GetUser().GetLogin(),
		State:         githubState(pr),
		ProviderState: pr.GetState(),
		CreatedAt:     pr.GetCreatedAt().Time,
		UpdatedAt:     pr.GetUpdatedAt().Time,
		Merged:        !pr.GetMergedAt().IsZero(),
		Draft:         pr.GetDraft(),
		Base:          pr.GetBase().GetRef(),
		Head:          pr.GetHead().GetRef(),
		URL:           pr.GetHTMLURL(),
	}
}

// gitlabPRDetails converts a GitLab merge request.
func gitlabPRDetails(mr *gitlab.BasicMergeRequest) *PRDetails {
	d := &PRDetails{
		Number:        int(mr.IID),
		Title:         mr.Title,
		Body:          mr.Description,
		State:         gitlabState(mr.State),
		ProviderState: mr.State,
		Merged:        mr.MergedAt != nil && !mr.MergedAt.IsZero(),
		Draft:         mr.Draft,
		Base:          mr.TargetBranch,
		Head:          mr.SourceBranch,
		URL:           mr.WebURL,
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

// githubState normalizes a GitHub PR state; GitHub reports merged PRs as
// closed.
func githubState(pr *githublib.PullRequest) string {
	switch {
	case pr.GetState() == "open":
		return StateOpen
	case !pr.GetMergedAt().IsZero() || pr.GetMerged():
		return StateMerged
	default:
		return StateClosed
	}
}

// gitlabState normalizes a GitLab MR state (opened, closed, locked, merged).
// "locked" is a transient state while GitLab merges the MR.
func gitlabState(state string) string {
	switch state {
	case "merged":
		return StateMerged
	case "closed":
		return StateClosed
	default:
		return StateOpen
	}
}

// gitlabProject is the project path ("group/sub/repo") used as GitLab project ID.
func gitlabProject(nfo *Info) string {
	return nfo.Owner + "/" + nfo.Repo
}

// Summary returns the list representation of a PR.
func (d *PRDetails) Summary() PullRequest {
	return PullRequest{
		Number: d.Number, Title: d.Title, Author: d.Author,
		State: d.State, ProviderState: d.ProviderState, Draft: d.Draft,
		Base: d.Base, Head: d.Head, CreatedAt: d.CreatedAt, URL: d.URL,
	}
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

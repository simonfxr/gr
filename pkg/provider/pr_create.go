package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// CreateOptions controls PR/MR creation.
type CreateOptions struct {
	Title string
	Body  string
	Base  string // target branch
	Head  string // source branch
	Draft bool
	// SquashByDefault indicates the PR/MR should default to squashing commits when merged
	// (provider support varies; ignored where unsupported)
	SquashByDefault  bool
	DeleteAfterMerge bool
}

// PrCreate creates a pull/merge request on the detected provider.
func (p Provider) PrCreate(ctx context.Context, nfo *Info, in CreateOptions) (*PRDetails, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prCreateGitHub(ctx, nfo, in)
	case ProviderGitLab:
		return prCreateGitLab(ctx, nfo, in)
	case ProviderBitbucket:
		return prCreateBitbucket(ctx, nfo, in)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prCreateGitHub(ctx context.Context, nfo *Info, in CreateOptions) (*PRDetails, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	head := strings.TrimSpace(in.Head)
	base := strings.TrimSpace(in.Base)
	if head == "" {
		h, err := CurrentBranch(nfo)
		if err != nil {
			return nil, err
		}
		head = h
	}
	if base == "" {
		b, err := DefaultBranchGitHub(ctx, gh, nfo)
		if err != nil {
			return nil, err
		}
		base = b
	}

	// Prevent duplicate open PRs for the same head
	prs, _, err := gh.PullRequests.List(ctx, nfo.Owner, nfo.Repo, &githublib.PullRequestListOptions{
		State:   "open",
		Head:    fmt.Sprintf("%s:%s", nfo.Owner, head),
		PerPage: 1,
	})
	if err == nil && len(prs) > 0 {
		return nil, fmt.Errorf("a pull request already exists for branch %q", head)
	}

	title, err := ResolvePRTitle(in.Title, nfo)
	if err != nil {
		return nil, err
	}

	npr := &githublib.NewPullRequest{
		Title: new(title),
		Head:  new(head),
		Base:  new(base),
		Body:  new(in.Body),
		Draft: new(in.Draft),
	}
	pr, _, err := gh.PullRequests.Create(ctx, nfo.Owner, nfo.Repo, npr)
	if err != nil {
		return nil, err
	}
	return githubPRDetails(pr), nil
}

func prCreateGitLab(ctx context.Context, nfo *Info, in CreateOptions) (*PRDetails, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	head := strings.TrimSpace(in.Head)
	base := strings.TrimSpace(in.Base)
	if head == "" {
		h, err := CurrentBranch(nfo)
		if err != nil {
			return nil, err
		}
		head = h
	}
	if base == "" {
		b, err := DefaultBranchGitLab(ctx, gl, nfo)
		if err != nil {
			return nil, err
		}
		base = b
	}

	// Check existing open MR for source branch
	project := gitlabProject(nfo)
	mrs, _, err := gl.MergeRequests.ListProjectMergeRequests(project, &gitlab.ListProjectMergeRequestsOptions{
		State:        new("opened"),
		SourceBranch: new(head),
		PerPage:      1, Page: 1,
	})
	if err == nil && len(mrs) > 0 {
		return nil, fmt.Errorf("a merge request already exists for branch %q", head)
	}

	title, err := ResolvePRTitle(in.Title, nfo)
	if err != nil {
		return nil, err
	}
	if in.Draft && !gitlabDraftTitle.MatchString(title) {
		// GitLab has no draft flag on creation; a title prefix marks drafts.
		title = "Draft: " + title
	}

	mr, _, err := gl.MergeRequests.CreateMergeRequest(project, &gitlab.CreateMergeRequestOptions{
		Title:        new(title),
		SourceBranch: new(head),
		TargetBranch: new(base),
		Description:  new(in.Body),
		// Enable squash by default unless opted out via CLI
		Squash:             new(in.SquashByDefault),
		RemoveSourceBranch: new(in.DeleteAfterMerge),
	})
	if err != nil {
		return nil, err
	}
	return gitlabPRDetails(&mr.BasicMergeRequest), nil
}

// gitlabDraftTitle matches the title prefixes by which GitLab marks drafts.
var gitlabDraftTitle = regexp.MustCompile(`(?i)^\s*(\[draft\]|\(draft\)|draft:|draft\s*-)`)

func prCreateBitbucket(ctx context.Context, nfo *Info, in CreateOptions) (*PRDetails, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	head := strings.TrimSpace(in.Head)
	base := strings.TrimSpace(in.Base)
	if head == "" {
		h, err := CurrentBranch(nfo)
		if err != nil {
			return nil, err
		}
		head = h
	}
	if base == "" {
		b, err := DefaultBranchBitbucket(ctx, bb, nfo)
		if err != nil {
			return nil, err
		}
		base = b
	}

	// Check existing open PR
	if prs, err := bitbucketOpenPRs(ctx, bb, nfo, "source.branch.name = "+bitbucketQueryString(head)); err == nil && len(prs) > 0 {
		return nil, fmt.Errorf("a pull request already exists for branch %q", head)
	}

	title, err := ResolvePRTitle(in.Title, nfo)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"title":               title,
		"description":         in.Body,
		"source":              map[string]any{"branch": map[string]any{"name": head}},
		"destination":         map[string]any{"branch": map[string]any{"name": base}},
		"draft":               in.Draft,
		"close_source_branch": in.DeleteAfterMerge,
	}
	var pr bitbucketPR
	if _, err := bb.call(ctx, http.MethodPost, bitbucketRepoEndpoint(nfo)+"/pullrequests", payload, &pr); err != nil {
		return nil, err
	}
	return pr.toPRDetails(), nil
}

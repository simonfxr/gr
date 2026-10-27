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
	gl, err := gitlabClient(nfo)
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

func prCreateBitbucket(ctx context.Context, nfo *Info, in CreateOptions) (*PRDetails, error) {
	bb, err := bitbucketClient(nfo)
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
	query := fmt.Sprintf("source.branch.name = \"%s\"", head)
	res, err := bb.Repositories.PullRequests.Gets(&bitbucket.PullRequestsOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
		States:   []string{"OPEN"},
		Query:    query,
	})
	if err == nil {
		if m, ok := res.(map[string]any); ok {
			if vals, _ := m["values"].([]any); len(vals) > 0 {
				return nil, fmt.Errorf("a pull request already exists for branch %q", head)
			}
		}
	}

	title, err := ResolvePRTitle(in.Title, nfo)
	if err != nil {
		return nil, err
	}

	po := &bitbucket.PullRequestsOptions{
		Owner:             nfo.Owner,
		RepoSlug:          nfo.Repo,
		Title:             title,
		Description:       in.Body,
		SourceBranch:      head,
		DestinationBranch: base,
		Draft:             in.Draft,
		CloseSourceBranch: in.DeleteAfterMerge,
	}
	createdRes, err := bb.Repositories.PullRequests.Create(po)
	if err != nil {
		return nil, err
	}
	pr, ok := createdRes.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response from bitbucket create")
	}
	// Parse into PRDetails
	prTitle, _ := pr["title"].(string)
	body, _ := pr["description"].(string)
	state, _ := pr["state"].(string)
	author := ""
	if au, ok := pr["author"].(map[string]any); ok {
		if n, ok := au["nickname"].(string); ok && n != "" {
			author = n
		} else if d, ok := au["display_name"].(string); ok {
			author = d
		}
	}
	createdStr, _ := pr["created_on"].(string)
	updatedStr, _ := pr["updated_on"].(string)
	created, _ := time.Parse(time.RFC3339, createdStr)
	updated, _ := time.Parse(time.RFC3339, updatedStr)
	url := ""
	if links, ok := pr["links"].(map[string]any); ok {
		if html, ok := links["html"].(map[string]any); ok {
			if href, ok := html["href"].(string); ok {
				url = href
			}
		}
	}
	num := 0
	if v, ok := pr["id"].(float64); ok {
		num = int(v)
	}
	return &PRDetails{
		Number:    num,
		Title:     prTitle,
		Body:      body,
		Author:    author,
		State:     strings.ToLower(state),
		CreatedAt: created,
		UpdatedAt: updated,
		Merged:    false,
		Draft:     in.Draft,
		Base:      base,
		Head:      head,
		URL:       url,
	}, nil
}

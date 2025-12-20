package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bitbucket "github.com/ktrysmt/go-bitbucket"
)

// PRDetails is a provider-agnostic view of a single PR/MR.
type PRDetails struct {
	Number    int
	Title     string
	Body      string
	Author    string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Merged    bool
	Draft     bool
	Base      string
	Head      string
	URL       string
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
	details := &PRDetails{
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
	}
	return details, nil
}

func prViewGitLab(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}

	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
	mr, _, err := gl.MergeRequests.GetMergeRequest(project, number, nil)
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

	details := &PRDetails{
		Number:    mr.IID,
		Title:     mr.Title,
		Body:      mr.Description,
		Author:    author,
		State:     mr.State,
		CreatedAt: created,
		UpdatedAt: updated,
		Merged:    merged,
		Draft:     false, // GitLab draft/WIP not standardized in this client; omit
		Base:      mr.TargetBranch,
		Head:      mr.SourceBranch,
		URL:       mr.WebURL,
	}
	return details, nil
}

func prViewBitbucket(ctx context.Context, nfo *Info, number int) (*PRDetails, error) {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	// Build options
	po := &bitbucket.PullRequestsOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
		ID:       fmt.Sprintf("%d", number),
	}
	_ = ctx
	res, err := bb.Repositories.PullRequests.Get(po)
	if err != nil {
		return nil, err
	}
	pr, ok := res.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response from bitbucket")
	}
	// Extract fields
	title, _ := pr["title"].(string)
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
	merged := strings.EqualFold(state, "MERGED")
	// Branches
	base := ""
	head := ""
	if dst, ok := pr["destination"].(map[string]any); ok {
		if br, ok := dst["branch"].(map[string]any); ok {
			if name, ok := br["name"].(string); ok {
				base = name
			}
		}
	}
	if src, ok := pr["source"].(map[string]any); ok {
		if br, ok := src["branch"].(map[string]any); ok {
			if name, ok := br["name"].(string); ok {
				head = name
			}
		}
	}
	url := ""
	if links, ok := pr["links"].(map[string]any); ok {
		if html, ok := links["html"].(map[string]any); ok {
			if href, ok := html["href"].(string); ok {
				url = href
			}
		}
	}
	// Number
	num := number
	if v, ok := pr["id"].(float64); ok {
		num = int(v)
	}
	return &PRDetails{
		Number:    num,
		Title:     title,
		Body:      body,
		Author:    author,
		State:     strings.ToLower(state),
		CreatedAt: created,
		UpdatedAt: updated,
		Merged:    merged,
		Draft:     false,
		Base:      base,
		Head:      head,
		URL:       url,
	}, nil
}

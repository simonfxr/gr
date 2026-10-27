package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// ListOptions are filters for PR list queries.
type ListOptions struct {
	State    string // open|closed|merged|all
	Author   string
	Assignee string
	Base     string
	Head     string
	Limit    int
}

// PullRequest is a provider-agnostic PR representation for output.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	URL       string    `json:"url"`
}

// PrList lists pull/merge requests for a detected provider.
func (p Provider) PrList(ctx context.Context, nfo *Info, opts ListOptions) ([]PullRequest, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prListGitHub(ctx, nfo, opts)
	case ProviderGitLab:
		return prListGitLab(ctx, nfo, opts)
	case ProviderBitbucket:
		return prListBitbucket(ctx, nfo, opts)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prListGitHub(ctx context.Context, nfo *Info, opts ListOptions) ([]PullRequest, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}

	state := strings.ToLower(strings.TrimSpace(opts.State))
	mergedOnly := false
	switch state {
	case "", "open":
		state = "open"
	case "closed":
		state = "closed"
	case "all":
		state = "all"
	case "merged":
		state = "closed"
		mergedOnly = true
	default:
		state = "open"
	}

	perPage := 30
	if opts.Limit > 0 && opts.Limit < perPage {
		perPage = opts.Limit
	} else if opts.Limit > 100 {
		perPage = 100
	} else if opts.Limit >= 30 {
		perPage = min(opts.Limit, 100)
	}

	head := ""
	if opts.Head != "" {
		// GitHub expects owner:branch for head filter
		head = fmt.Sprintf("%s:%s", nfo.Owner, opts.Head)
	}

	ghOpts := &githublib.PullRequestListOptions{
		State:   state,
		Base:    opts.Base,
		Head:    head,
		PerPage: perPage,
	}

	var results []PullRequest
	page := 1
	for {
		ghOpts.Page = page
		prs, resp, err := gh.PullRequests.List(ctx, nfo.Owner, nfo.Repo, ghOpts)
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			if mergedOnly {
				if pr.MergedAt == nil || pr.MergedAt.IsZero() {
					continue
				}
			}
			if opts.Author != "" {
				if pr.User == nil || strings.ToLower(pr.User.GetLogin()) != strings.ToLower(opts.Author) {
					continue
				}
			}
			if opts.Assignee != "" {
				ok := false
				if pr.Assignee != nil && strings.EqualFold(pr.Assignee.GetLogin(), opts.Assignee) {
					ok = true
				}
				if !ok && pr.Assignees != nil {
					for _, a := range pr.Assignees {
						if a != nil && strings.EqualFold(a.GetLogin(), opts.Assignee) {
							ok = true
							break
						}
					}
				}
				if !ok {
					continue
				}
			}
			results = append(results, githubPRDetails(pr).Summary())
			if opts.Limit > 0 && len(results) >= opts.Limit {
				return results, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return results, nil
}

func prListGitLab(ctx context.Context, nfo *Info, opts ListOptions) ([]PullRequest, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}

	state := strings.ToLower(strings.TrimSpace(opts.State))
	// GitLab states: opened, closed, merged; omit for all
	var glState *string
	switch state {
	case "open", "opened", "":
		s := "opened"
		glState = &s
	case "closed":
		s := "closed"
		glState = &s
	case "merged":
		s := "merged"
		glState = &s
	case "all":
		glState = nil
	default:
		s := "opened"
		glState = &s
	}

	perPage := 20
	if opts.Limit > 0 && opts.Limit < perPage {
		perPage = opts.Limit
	} else if opts.Limit > 0 && opts.Limit > perPage {
		perPage = min(opts.Limit, 100)
	}

	listOpts := &gitlab.ListProjectMergeRequestsOptions{
		PerPage: int64(perPage), Page: 1,
	}
	if glState != nil {
		listOpts.State = glState
	}
	if opts.Author != "" {
		listOpts.AuthorUsername = new(opts.Author)
	}
	if opts.Base != "" {
		listOpts.TargetBranch = new(opts.Base)
	}
	if opts.Head != "" {
		listOpts.SourceBranch = new(opts.Head)
	}

	project := gitlabProject(nfo)
	var results []PullRequest
	for {
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, listOpts)
		if err != nil {
			return nil, err
		}
		for _, mr := range mrs {
			if opts.Assignee != "" {
				// Client-side filter by assignee username (Assignee or Assignees)
				ok := false
				if mr.Assignee != nil && strings.EqualFold(mr.Assignee.Username, opts.Assignee) {
					ok = true
				}
				if !ok && len(mr.Assignees) > 0 {
					for _, a := range mr.Assignees {
						if a != nil && strings.EqualFold(a.Username, opts.Assignee) {
							ok = true
							break
						}
					}
				}
				if !ok {
					continue
				}
			}
			results = append(results, gitlabPRDetails(mr).Summary())
			if opts.Limit > 0 && len(results) >= opts.Limit {
				return results, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		listOpts.Page = resp.NextPage
	}
	return results, nil
}

// The pullrequests endpoint caps pagelen at 50, unlike the generic API cap of
// 100. Larger values return HTTP 400 "Invalid pagelen".
const bitbucketPRMaxPageSize = 50

func prListBitbucket(ctx context.Context, nfo *Info, opts ListOptions) ([]PullRequest, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	params := url.Values{
		"sort": {"-created_on"},
		"fields": {"next,values.id,values.title,values.state,values.created_on," +
			"values.author.nickname,values.author.display_name,values.links.html.href"},
	}
	// Omission means OPEN, not all. Use repeated state parameters rather than
	// the SDK's States field, which overwrites all but the last state.
	switch strings.ToLower(strings.TrimSpace(opts.State)) {
	case "merged":
		params["state"] = []string{"MERGED"}
	case "closed":
		params["state"] = []string{"MERGED", "DECLINED", "SUPERSEDED"}
	case "all":
		params["state"] = []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}
	default:
		params["state"] = []string{"OPEN"}
	}
	var qs []string
	if opts.Base != "" {
		qs = append(qs, "destination.branch.name = "+bitbucketQueryString(opts.Base))
	}
	if opts.Head != "" {
		qs = append(qs, "source.branch.name = "+bitbucketQueryString(opts.Head))
	}
	baseQuery := strings.Join(qs, " AND ")
	authorName := ""
	if opts.Author != "" {
		switch {
		case strings.HasPrefix(opts.Author, "{") && strings.HasSuffix(opts.Author, "}"):
			qs = append(qs, "author.uuid = "+bitbucketQueryString(opts.Author))
		case strings.Contains(opts.Author, ":"):
			qs = append(qs, "author.account_id = "+bitbucketQueryString(opts.Author))
		default:
			authorName = opts.Author
			// Contains is case-insensitive; retain exact EqualFold matching
			// locally. Do not enumerate workspace members for each invocation.
			literal := bitbucketQueryString(authorName)
			qs = append(qs, "(author.nickname ~ "+literal+" OR author.display_name ~ "+literal+")")
		}
	}
	if len(qs) > 0 {
		params.Set("q", strings.Join(qs, " AND "))
	}
	pageSize := bitbucketPRMaxPageSize
	if opts.Limit > 0 {
		pageSize = max(10, min(opts.Limit, bitbucketPRMaxPageSize))
	}
	params.Set("pagelen", strconv.Itoa(pageSize))
	// Bitbucket Cloud does not have PR assignees; preserve existing behavior.
	endpoint := bitbucketRepoEndpoint(nfo) + "/pullrequests"
	var out []PullRequest
	collect := func(pr bitbucketPR) bool {
		if authorName != "" && (pr.Author == nil ||
			!strings.EqualFold(pr.Author.Nickname, authorName) && !strings.EqualFold(pr.Author.DisplayName, authorName)) {
			return true
		}
		out = append(out, pr.toPRDetails().Summary())
		return opts.Limit <= 0 || len(out) < opts.Limit
	}
	err = bitbucketListPages(ctx, bb, endpoint, params, collect)
	var apiErr *bitbucketAPIError
	if authorName != "" && errors.As(err, &apiErr) && apiErr.status == http.StatusBadRequest {
		// Some deployments reject queryable user-name fields. Retry without
		// that optimization, keeping state/base/head and exact local matching.
		// Other failures (auth, rate limits, server errors) must not be hidden.
		params.Del("q")
		if baseQuery != "" {
			params.Set("q", baseQuery)
		}
		params.Set("pagelen", strconv.Itoa(bitbucketPRMaxPageSize))
		out = nil
		err = bitbucketListPages(ctx, bb, endpoint, params, collect)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

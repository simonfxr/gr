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
	Number    int
	Title     string
	Author    string
	State     string
	CreatedAt time.Time
	URL       string
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
		State: state,
		Base:  opts.Base,
		Head:  head,
		ListOptions: githublib.ListOptions{
			PerPage: perPage,
		},
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
			created := time.Time{}
			if pr.CreatedAt != nil {
				created = pr.CreatedAt.Time
			}
			results = append(results, PullRequest{
				Number:    pr.GetNumber(),
				Title:     pr.GetTitle(),
				Author:    pr.GetUser().GetLogin(),
				State:     pr.GetState(),
				CreatedAt: created,
				URL:       pr.GetHTMLURL(),
			})
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
	gl, err := gitlabClient(nfo)
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
		ListOptions: gitlab.ListOptions{PerPage: int64(perPage), Page: 1},
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

	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
	var results []PullRequest
	for {
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, listOpts)
		if err != nil {
			return nil, err
		}
		for _, mr := range mrs {
			author := ""
			if mr.Author != nil {
				author = mr.Author.Username
			}
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
			created := time.Time{}
			if mr.CreatedAt != nil {
				created = *mr.CreatedAt
			}
			results = append(results, PullRequest{
				Number:    int(mr.IID),
				Title:     mr.Title,
				Author:    author,
				State:     mr.State,
				CreatedAt: created,
				URL:       mr.WebURL,
			})
			if opts.Limit > 0 && len(results) >= opts.Limit {
				return results, nil
			}
		}
		if resp.CurrentPage >= resp.TotalPages || resp.NextPage == 0 {
			break
		}
		listOpts.Page = resp.NextPage
	}
	return results, nil
}

func prListBitbucket(ctx context.Context, nfo *Info, opts ListOptions) ([]PullRequest, error) {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	// Map state to query filter (library has bug with States field - uses Set instead of Add)
	var stateQuery string
	switch strings.ToLower(strings.TrimSpace(opts.State)) {
	case "", "open":
		stateQuery = `state = "OPEN"`
	case "merged":
		stateQuery = `state = "MERGED"`
	case "closed":
		stateQuery = `state = "MERGED" OR state = "DECLINED" OR state = "SUPERSEDED"`
	case "all":
		stateQuery = ""
	default:
		stateQuery = `state = "OPEN"`
	}

	// Build query string for filters not directly supported
	var qs []string
	if stateQuery != "" {
		qs = append(qs, "("+stateQuery+")")
	}
	// Author filter: Bitbucket deprecated username. Use uuid or account_id server-side,
	// display names are resolved via workspace members API.
	authorClientFilter := ""
	if opts.Author != "" {
		if strings.HasPrefix(opts.Author, "{") && strings.HasSuffix(opts.Author, "}") {
			// UUID format
			qs = append(qs, fmt.Sprintf("author.uuid = \"%s\"", opts.Author))
		} else if strings.Contains(opts.Author, ":") {
			// account_id format (e.g., "557058:xxx")
			qs = append(qs, fmt.Sprintf("author.account_id = \"%s\"", opts.Author))
		} else {
			// Try to resolve display name/nickname to UUID via workspace members
			if uuid := resolveWorkspaceMemberUUID(bb, nfo.Owner, opts.Author); uuid != "" {
				qs = append(qs, fmt.Sprintf("author.uuid = \"%s\"", uuid))
			} else {
				// Fallback to client-side filtering
				authorClientFilter = opts.Author
			}
		}
	}
	if opts.Base != "" {
		qs = append(qs, fmt.Sprintf("destination.branch.name = \"%s\"", opts.Base))
	}
	if opts.Head != "" {
		qs = append(qs, fmt.Sprintf("source.branch.name = \"%s\"", opts.Head))
	}
	// Bitbucket Cloud does not have assignee on PRs; ignore Assignee filter for now.

	po := &bitbucket.PullRequestsOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
		Query:    strings.Join(qs, " AND "),
	}

	// Context isn't used by this library's Gets, but keep symmetry
	_ = ctx

	res, err := bb.Repositories.PullRequests.Gets(po)
	if err != nil {
		return nil, err
	}
	m, ok := res.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from bitbucket")
	}
	vals, _ := m["values"].([]any)
	var out []PullRequest
	for _, it := range vals {
		pr, _ := it.(map[string]any)
		if pr == nil {
			continue
		}
		// Number
		num := 0
		if v, ok := pr["id"].(float64); ok {
			num = int(v)
		}
		// Title
		title, _ := pr["title"].(string)
		// State
		state, _ := pr["state"].(string)
		state = strings.ToLower(state)
		// Author (try nickname, then display_name)
		author := ""
		nickname := ""
		displayName := ""
		if au, ok := pr["author"].(map[string]any); ok {
			nickname, _ = au["nickname"].(string)
			displayName, _ = au["display_name"].(string)
			if nickname != "" {
				author = nickname
			} else {
				author = displayName
			}
		}
		// Client-side author filter (for display names/nicknames)
		if authorClientFilter != "" {
			if !strings.EqualFold(author, authorClientFilter) && !strings.EqualFold(displayName, authorClientFilter) && !strings.EqualFold(nickname, authorClientFilter) {
				continue
			}
		}
		// CreatedAt
		createdStr, _ := pr["created_on"].(string)
		created, _ := time.Parse(time.RFC3339, createdStr)
		// URL
		url := ""
		if links, ok := pr["links"].(map[string]any); ok {
			if html, ok := links["html"].(map[string]any); ok {
				if href, ok := html["href"].(string); ok {
					url = href
				}
			}
		}
		out = append(out, PullRequest{
			Number:    num,
			Title:     title,
			Author:    author,
			State:     state,
			CreatedAt: created,
			URL:       url,
		})
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

// resolveWorkspaceMemberUUID looks up a user by display_name or nickname in workspace members.
func resolveWorkspaceMemberUUID(bb *bitbucket.Client, workspace, name string) string {
	res, err := bb.Workspaces.Members(workspace)
	if err != nil || res == nil {
		return ""
	}
	for _, user := range res.Members {
		if strings.EqualFold(user.DisplayName, name) || strings.EqualFold(user.Nickname, name) {
			return user.Uuid
		}
	}
	return ""
}

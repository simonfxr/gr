package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	githublib "github.com/google/go-github/v74/github"
)

// GitHub has two kinds of PR comments: conversation comments (the issue
// comments of the PR), which are neither threaded nor resolvable, and review
// comments on the diff, which form resolvable threads. Thread resolution is
// only available through the GraphQL API.

func prCommentsGitHub(ctx context.Context, nfo *Info, number int) ([]PRComment, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	out := []PRComment{}
	issueOpts := &githublib.IssueListCommentsOptions{PerPage: 100}
	for {
		page, resp, err := gh.Issues.ListComments(ctx, nfo.Owner, nfo.Repo, number, issueOpts)
		if err != nil {
			return nil, err
		}
		for _, c := range page {
			out = append(out, githubIssueComment(c))
		}
		if resp.NextPage == 0 {
			break
		}
		issueOpts.Page = resp.NextPage
	}
	threads, err := githubReviewThreads(ctx, gh, nfo, number)
	if err != nil {
		return nil, err
	}
	reviewOpts := &githublib.PullRequestListCommentsOptions{PerPage: 100}
	for {
		page, resp, err := gh.PullRequests.ListComments(ctx, nfo.Owner, nfo.Repo, number, reviewOpts)
		if err != nil {
			return nil, err
		}
		for _, c := range page {
			out = append(out, githubReviewComment(c, threads))
		}
		if resp.NextPage == 0 {
			break
		}
		reviewOpts.Page = resp.NextPage
	}
	slices.SortStableFunc(out, func(a, b PRComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

func prAddCommentGitHub(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) (*PRComment, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	pr, _, err := gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
	if err != nil {
		return nil, err
	}
	c, _, err := gh.PullRequests.CreateComment(ctx, nfo.Owner, nfo.Repo, number, &githublib.PullRequestComment{
		Body:     new(opts.Body),
		CommitID: new(pr.GetHead().GetSHA()),
		Path:     new(opts.Path),
		Line:     new(opts.Line),
		Side:     new("RIGHT"),
	})
	if err != nil {
		return nil, err
	}
	out := githubReviewComment(c, nil)
	out.Resolvable = true // a new thread
	return &out, nil
}

// prReplyGitHub replies in the thread of a review comment. Conversation
// comments have no threads; replies to them quote the original instead,
// like GitHub's "Quote reply".
func prReplyGitHub(ctx context.Context, nfo *Info, number, commentID int, body string) (*PRComment, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	review, _, err := gh.PullRequests.GetComment(ctx, nfo.Owner, nfo.Repo, int64(commentID))
	if err == nil {
		// Replies to replies go to the thread root, as GitHub only has one level.
		root := cmp.Or(review.GetInReplyTo(), review.GetID())
		c, _, err := gh.PullRequests.CreateCommentInReplyTo(ctx, nfo.Owner, nfo.Repo, number, body, root)
		if err != nil {
			return nil, err
		}
		out := githubReviewComment(c, nil)
		return &out, nil
	}
	if !IsNotFound(err) {
		return nil, err
	}
	issue, _, err := gh.Issues.GetComment(ctx, nfo.Owner, nfo.Repo, int64(commentID))
	if IsNotFound(err) {
		return nil, notFoundf("comment %d not found on pull request #%d", commentID, number)
	} else if err != nil {
		return nil, err
	}
	quoted := fmt.Sprintf("> @%s wrote:\n>\n%s\n\n%s", issue.GetUser().GetLogin(), quoteMarkdown(issue.GetBody()), body)
	c, _, err := gh.Issues.CreateComment(ctx, nfo.Owner, nfo.Repo, number, &githublib.IssueComment{Body: new(quoted)})
	if err != nil {
		return nil, err
	}
	out := githubIssueComment(c)
	return &out, nil
}

func prResolveGitHub(ctx context.Context, nfo *Info, number, commentID int) error {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return err
	}
	threads, err := githubReviewThreads(ctx, gh, nfo, number)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if !slices.Contains(t.commentIDs, int64(commentID)) {
			continue
		}
		if t.resolved {
			return nil
		}
		var res struct {
			ResolveReviewThread struct {
				Thread struct{ IsResolved bool }
			}
		}
		return githubGraphQL(ctx, gh, `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }`,
			map[string]any{"id": t.id}, &res)
	}
	if _, _, err := gh.Issues.GetComment(ctx, nfo.Owner, nfo.Repo, int64(commentID)); err == nil {
		return invalidArgf("comment %d is a conversation comment; GitHub can only resolve review threads", commentID)
	}
	return notFoundf("review comment %d not found on pull request #%d", commentID, number)
}

func githubIssueComment(c *githublib.IssueComment) PRComment {
	return PRComment{
		ID:        int(c.GetID()),
		Author:    c.GetUser().GetLogin(),
		Body:      c.GetBody(),
		CreatedAt: c.GetCreatedAt().Time,
		URL:       c.GetHTMLURL(),
	}
}

// githubReviewComment converts a review comment; threads (by root comment
// ID) supplies the resolution state.
func githubReviewComment(c *githublib.PullRequestComment, threads map[int64]githubThread) PRComment {
	out := PRComment{
		ID:        int(c.GetID()),
		Author:    c.GetUser().GetLogin(),
		Body:      c.GetBody(),
		CreatedAt: c.GetCreatedAt().Time,
		Path:      c.GetPath(),
		Line:      cmp.Or(c.GetLine(), c.GetOriginalLine()),
		URL:       c.GetHTMLURL(),
		ParentID:  int(c.GetInReplyTo()),
	}
	root := cmp.Or(c.GetInReplyTo(), c.GetID())
	if t, ok := threads[root]; ok {
		out.Resolvable = out.ParentID == 0
		out.Resolved = t.resolved
	}
	return out
}

// githubThread is a review thread: its GraphQL node ID, resolution state
// and the database IDs of its comments (the first is the root).
type githubThread struct {
	id         string
	resolved   bool
	commentIDs []int64
}

// githubReviewThreads returns the review threads of a PR by root comment ID.
func githubReviewThreads(ctx context.Context, gh *githublib.Client, nfo *Info, number int) (map[int64]githubThread, error) {
	const query = `query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { id isResolved comments(first: 100) { nodes { databaseId } } }
      }
    }
  }
}`
	threads := make(map[int64]githubThread)
	vars := map[string]any{"owner": nfo.Owner, "repo": nfo.Repo, "number": number, "cursor": nil}
	for {
		var res struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool
							EndCursor   string
						}
						Nodes []struct {
							ID         string
							IsResolved bool
							Comments   struct {
								Nodes []struct{ DatabaseID int64 }
							}
						}
					}
				}
			}
		}
		if err := githubGraphQL(ctx, gh, query, vars, &res); err != nil {
			return nil, err
		}
		if res.Repository == nil || res.Repository.PullRequest == nil {
			return nil, notFoundf("pull request #%d not found", number)
		}
		rt := res.Repository.PullRequest.ReviewThreads
		for _, n := range rt.Nodes {
			t := githubThread{id: n.ID, resolved: n.IsResolved}
			for _, c := range n.Comments.Nodes {
				t.commentIDs = append(t.commentIDs, c.DatabaseID)
			}
			if len(t.commentIDs) > 0 {
				threads[t.commentIDs[0]] = t
			}
		}
		if !rt.PageInfo.HasNextPage {
			return threads, nil
		}
		vars["cursor"] = rt.PageInfo.EndCursor
	}
}

// githubGraphQL runs a GraphQL request with the REST client's credentials.
// GitHub Enterprise serves GraphQL at /api/graphql next to /api/v3.
func githubGraphQL(ctx context.Context, gh *githublib.Client, query string, vars map[string]any, data any) error {
	endpoint := "graphql"
	if base := gh.BaseURL.Path; strings.HasSuffix(base, "/api/v3/") {
		endpoint = strings.TrimSuffix(base, "v3/") + "graphql"
	}
	req, err := gh.NewRequest(http.MethodPost, endpoint, map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := gh.Do(ctx, req, &res); err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		if res.Errors[0].Type == "NOT_FOUND" {
			return notFoundf("github: %s", res.Errors[0].Message)
		}
		return fmt.Errorf("github GraphQL: %s", res.Errors[0].Message)
	}
	return json.Unmarshal(res.Data, data)
}

// quoteMarkdown prefixes every line with "> ".
func quoteMarkdown(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

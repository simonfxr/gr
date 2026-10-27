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
	var review []*githublib.PullRequestComment
	reviewOpts := &githublib.PullRequestListCommentsOptions{PerPage: 100}
	for {
		page, resp, err := gh.PullRequests.ListComments(ctx, nfo.Owner, nfo.Repo, number, reviewOpts)
		if err != nil {
			return nil, err
		}
		review = append(review, page...)
		if resp.NextPage == 0 {
			break
		}
		reviewOpts.Page = resp.NextPage
	}
	// Threads are keyed by the node ID of their root comment.
	nodeIDs := make(map[int64]string, len(review))
	for _, c := range review {
		nodeIDs[c.GetID()] = c.GetNodeID()
	}
	for _, c := range review {
		root := nodeIDs[cmp.Or(c.GetInReplyTo(), c.GetID())]
		out = append(out, githubReviewComment(c, threads[root]))
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
	review, issue, err := githubGetComment(ctx, gh, nfo, number, commentID)
	if err != nil {
		return nil, err
	}
	if review != nil {
		// Replies to replies go to the thread root, as GitHub only has one level.
		root := cmp.Or(review.GetInReplyTo(), review.GetID())
		c, _, err := gh.PullRequests.CreateCommentInReplyTo(ctx, nfo.Owner, nfo.Repo, number, body, root)
		if err != nil {
			return nil, err
		}
		out := githubReviewComment(c, nil)
		return &out, nil
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
	review, _, err := githubGetComment(ctx, gh, nfo, number, commentID)
	if err != nil {
		return err
	}
	if review == nil {
		return invalidArgf("comment %d is a conversation comment; GitHub can only resolve review threads", commentID)
	}
	root := review
	if review.GetInReplyTo() != 0 {
		if root, _, err = gh.PullRequests.GetComment(ctx, nfo.Owner, nfo.Repo, review.GetInReplyTo()); err != nil {
			return err
		}
	}
	threads, err := githubReviewThreads(ctx, gh, nfo, number)
	if err != nil {
		return err
	}
	t, ok := threads[root.GetNodeID()]
	if !ok {
		return notFoundf("no review thread found for comment %d on pull request #%d", commentID, number)
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

// githubGetComment looks up a comment of a PR by ID: either a review comment
// or, failing that, a conversation (issue) comment. Comment IDs are unique
// per repository, so a comment of another PR or issue is reported as not
// found.
func githubGetComment(ctx context.Context, gh *githublib.Client, nfo *Info, number, commentID int) (*githublib.PullRequestComment, *githublib.IssueComment, error) {
	notFound := notFoundf("comment %d not found on pull request #%d", commentID, number)
	review, _, err := gh.PullRequests.GetComment(ctx, nfo.Owner, nfo.Repo, int64(commentID))
	if err == nil {
		if !githubURLOf(review.GetPullRequestURL(), "pulls", number) {
			return nil, nil, notFound
		}
		return review, nil, nil
	}
	if !IsNotFound(err) {
		return nil, nil, err
	}
	issue, _, err := gh.Issues.GetComment(ctx, nfo.Owner, nfo.Repo, int64(commentID))
	if IsNotFound(err) || (err == nil && !githubURLOf(issue.GetIssueURL(), "issues", number)) {
		return nil, nil, notFound
	} else if err != nil {
		return nil, nil, err
	}
	return nil, issue, nil
}

// githubURLOf reports whether an API URL such as a comment's
// pull_request_url refers to PR/issue number. An absent URL is accepted.
func githubURLOf(apiURL, kind string, number int) bool {
	return apiURL == "" || strings.HasSuffix(apiURL, fmt.Sprintf("/%s/%d", kind, number))
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

// githubReviewComment converts a review comment; thread is the review thread
// it belongs to, if known.
func githubReviewComment(c *githublib.PullRequestComment, thread *githubThread) PRComment {
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
	if thread != nil {
		out.Resolvable = out.ParentID == 0
		out.Resolved = thread.resolved
	}
	return out
}

// githubThread is a review thread: its GraphQL node ID and resolution state.
type githubThread struct {
	id       string
	resolved bool
}

// githubReviewThreads returns the review threads of a PR by the node ID of
// their root (first) comment. Node IDs are used because GraphQL's 32-bit
// databaseId cannot hold all REST comment IDs.
func githubReviewThreads(ctx context.Context, gh *githublib.Client, nfo *Info, number int) (map[string]*githubThread, error) {
	const query = `query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { id isResolved comments(first: 1) { nodes { id } } }
      }
    }
  }
}`
	threads := make(map[string]*githubThread)
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
								Nodes []struct{ ID string }
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
			if len(n.Comments.Nodes) > 0 && n.Comments.Nodes[0].ID != "" {
				threads[n.Comments.Nodes[0].ID] = &githubThread{id: n.ID, resolved: n.IsResolved}
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

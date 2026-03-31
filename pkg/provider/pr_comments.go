package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PRComment represents a single review comment on a PR/MR.
type PRComment struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// Optional location for inline comments (when available)
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	URL  string `json:"url,omitempty"`
}

// PrComments lists review comments for a PR number. For now only GitLab is supported.
func (p Provider) PrComments(ctx context.Context, nfo *Info, number int) ([]PRComment, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitLab:
		return prCommentsGitLab(ctx, nfo, number)
	case ProviderGitHub:
		return nil, errors.New("pr comments: GitHub backend not implemented yet")
	case ProviderBitbucket:
		return prCommentsBitbucket(ctx, nfo, number)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prCommentsGitLab(ctx context.Context, nfo *Info, number int) ([]PRComment, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)

	// Use Notes API for MR. Filter out system notes to get human review comments.
	perPage := int64(100)
	opts := &gitlab.ListMergeRequestNotesOptions{ListOptions: gitlab.ListOptions{PerPage: perPage, Page: 1}}
	var out []PRComment
	for {
		notes, resp, err := gl.Notes.ListMergeRequestNotes(project, int64(number), opts)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			if n == nil {
				continue
			}
			if n.System {
				continue // skip system-generated notes
			}
			author := strings.TrimSpace(n.Author.Username)
			if author == "" {
				author = strings.TrimSpace(n.Author.Name)
			}
			created := time.Time{}
			if n.CreatedAt != nil {
				created = *n.CreatedAt
			}
			pc := PRComment{ID: int(n.ID), Author: author, Body: n.Body, CreatedAt: created}
			// Try to attach path/line when available via Position
			if n.Position != nil {
				if p := n.Position.NewPath; p != "" {
					pc.Path = p
				} else if p := n.Position.OldPath; p != "" {
					pc.Path = p
				}
				if n.Position.NewLine != 0 {
					pc.Line = int(n.Position.NewLine)
				} else if n.Position.OldLine != 0 {
					pc.Line = int(n.Position.OldLine)
				}
			}
			// Construct a URL to the note if possible
			// Fetch MR once to get WebURL when first time
			// Optimize: we can build from base URL and IIDs if needed; keep simple by fetching once.
			out = append(out, pc)
		}
		if resp == nil || resp.CurrentPage >= resp.TotalPages || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func prCommentsBitbucket(ctx context.Context, nfo *Info, number int) ([]PRComment, error) {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	_ = ctx
	po := &bitbucket.PullRequestsOptions{
		Owner:    nfo.Owner,
		RepoSlug: nfo.Repo,
		ID:       fmt.Sprintf("%d", number),
	}
	res, err := bb.Repositories.PullRequests.GetComments(po)
	if err != nil {
		return nil, err
	}
	m, ok := res.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response from bitbucket")
	}
	vals, _ := m["values"].([]any)
	var out []PRComment
	for _, v := range vals {
		c, _ := v.(map[string]any)
		if c == nil {
			continue
		}
		// Skip deleted comments
		if deleted, _ := c["deleted"].(bool); deleted {
			continue
		}
		id := 0
		if fid, ok := c["id"].(float64); ok {
			id = int(fid)
		}
		body := ""
		if content, ok := c["content"].(map[string]any); ok {
			body, _ = content["raw"].(string)
		}
		author := ""
		if user, ok := c["user"].(map[string]any); ok {
			if n, ok := user["nickname"].(string); ok && n != "" {
				author = n
			} else if d, ok := user["display_name"].(string); ok {
				author = d
			}
		}
		createdStr, _ := c["created_on"].(string)
		created, _ := time.Parse(time.RFC3339, createdStr)
		// Inline location
		var path string
		var line int
		if inline, ok := c["inline"].(map[string]any); ok {
			path, _ = inline["path"].(string)
			if l, ok := inline["to"].(float64); ok && l > 0 {
				line = int(l)
			} else if l, ok := inline["from"].(float64); ok && l > 0 {
				line = int(l)
			}
		}
		// URL
		url := ""
		if links, ok := c["links"].(map[string]any); ok {
			if html, ok := links["html"].(map[string]any); ok {
				url, _ = html["href"].(string)
			}
		}
		out = append(out, PRComment{
			ID:        id,
			Author:    author,
			Body:      body,
			CreatedAt: created,
			Path:      path,
			Line:      line,
			URL:       url,
		})
	}
	return out, nil
}

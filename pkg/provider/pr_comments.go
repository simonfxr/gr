package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PRComment represents a single review comment on a PR/MR.
type PRComment struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Resolved  bool      `json:"resolved"`
	// Optional location for inline comments (when available)
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	URL  string `json:"url,omitempty"`
	// ParentID is set for replies.
	ParentID int `json:"parent_id,omitempty"`
}

// PrComments lists review comments for a PR number on GitLab or Bitbucket.
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
	project := gitlabProject(nfo)

	// Use Notes API for MR. Filter out system notes to get human review comments.
	perPage := int64(100)
	opts := &gitlab.ListMergeRequestNotesOptions{PerPage: perPage, Page: 1}
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
			pc := PRComment{ID: int(n.ID), Author: author, Body: n.Body, CreatedAt: created, Resolved: n.Resolved}
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
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	endpoint := bitbucketPREndpoint(nfo, number) + "/comments"
	out := []PRComment{}
	err = bitbucketListPages(ctx, bb, endpoint, url.Values{}, func(c bitbucketComment) bool {
		if !c.Deleted {
			out = append(out, c.toPRComment())
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// bitbucketUser is a Bitbucket account reference; name prefers the nickname.
type bitbucketUser struct {
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
}

func (u *bitbucketUser) name() string {
	if u == nil {
		return ""
	}
	if u.Nickname != "" {
		return u.Nickname
	}
	return u.DisplayName
}

// bitbucketComment is the subset of a Bitbucket PR comment that gr uses.
type bitbucketComment struct {
	ID      int `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User      *bitbucketUser `json:"user"`
	CreatedOn string         `json:"created_on"`
	Deleted   bool           `json:"deleted"`
	Inline    *struct {
		Path string `json:"path"`
		From int    `json:"from"`
		To   int    `json:"to"`
	} `json:"inline"`
	Parent *struct {
		ID int `json:"id"`
	} `json:"parent"`
	// Bitbucket includes a resolution object only when the comment is resolved.
	Resolution json.RawMessage `json:"resolution"`
	Links      struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (c *bitbucketComment) toPRComment() PRComment {
	out := PRComment{
		ID:       c.ID,
		Author:   c.User.name(),
		Body:     c.Content.Raw,
		Resolved: len(c.Resolution) > 0 && string(c.Resolution) != "null",
		URL:      c.Links.HTML.Href,
	}
	out.CreatedAt, _ = time.Parse(time.RFC3339, c.CreatedOn)
	if c.Inline != nil {
		out.Path = c.Inline.Path
		if c.Inline.To > 0 {
			out.Line = c.Inline.To
		} else if c.Inline.From > 0 {
			out.Line = c.Inline.From
		}
	}
	if c.Parent != nil {
		out.ParentID = c.Parent.ID
	}
	return out
}

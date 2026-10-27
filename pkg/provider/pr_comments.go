package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// PRComment represents a single review comment on a PR/MR.
type PRComment struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// Resolvable is set on thread roots that can be resolved; Resolved
	// then tells whether the thread is.
	Resolvable bool `json:"resolvable"`
	Resolved   bool `json:"resolved"`
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
		return prCommentsGitHub(ctx, nfo, number)
	case ProviderBitbucket:
		return prCommentsBitbucket(ctx, nfo, number)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
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
	} else {
		out.Resolvable = true // Bitbucket resolves top-level comment threads
	}
	return out
}

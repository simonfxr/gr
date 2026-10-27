package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// AddCommentOptions holds parameters for adding an inline PR comment.
type AddCommentOptions struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// PrAddComment adds an inline review comment to a PR.
func (p Provider) PrAddComment(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) (*PRComment, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	if strings.TrimSpace(opts.Body) == "" {
		return nil, invalidArgf("comment body must not be empty")
	}
	if strings.TrimSpace(opts.Path) == "" || opts.Line <= 0 {
		return nil, invalidArgf("comment needs a path and a positive line number")
	}
	switch p {
	case ProviderBitbucket:
		return bitbucketPostComment(ctx, nfo, number, map[string]any{
			"content": map[string]any{"raw": opts.Body},
			"inline":  map[string]any{"to": opts.Line, "path": opts.Path},
		})
	case ProviderGitHub:
		return prAddCommentGitHub(ctx, nfo, number, opts)
	case ProviderGitLab:
		return prAddCommentGitLab(ctx, nfo, number, opts)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

// bitbucketPostComment creates a PR comment and returns it as reported by Bitbucket.
func bitbucketPostComment(ctx context.Context, nfo *Info, number int, payload map[string]any) (*PRComment, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	u, err := bb.endpointURL(bitbucketPREndpoint(nfo, number) + "/comments")
	if err != nil {
		return nil, err
	}
	var res bitbucketComment
	if err := bb.do(ctx, http.MethodPost, u, payload, &res); err != nil {
		return nil, err
	}
	c := res.toPRComment()
	return &c, nil
}

package provider

import (
	"context"
	"errors"
	"strings"
)

// PrReplyComment posts a reply to an existing PR comment and returns the reply.
func (p Provider) PrReplyComment(ctx context.Context, nfo *Info, number, commentID int, body string) (*PRComment, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	if strings.TrimSpace(body) == "" {
		return nil, invalidArgf("reply body must not be empty")
	}
	switch p {
	case ProviderBitbucket:
		return bitbucketPostComment(ctx, nfo, number, map[string]any{
			"content": map[string]any{"raw": body},
			"parent":  map[string]any{"id": commentID},
		})
	default:
		return nil, unsupported("pr reply", p)
	}
}

package provider

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// PrResolveComment marks a PR comment as resolved.
func (p Provider) PrResolveComment(ctx context.Context, nfo *Info, number, commentID int) error {
	if nfo == nil {
		return errors.New("missing repo info")
	}
	switch p {
	case ProviderBitbucket:
		return prResolveCommentBitbucket(ctx, nfo, number, commentID)
	default:
		return unsupported("pr resolve", p)
	}
}

func prResolveCommentBitbucket(ctx context.Context, nfo *Info, number, commentID int) error {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return err
	}
	endpoint := bitbucketPREndpoint(nfo, number) + "/comments/" + strconv.Itoa(commentID) + "/resolve"
	_, err = bb.call(ctx, http.MethodPost, endpoint, nil, nil)
	return err
}

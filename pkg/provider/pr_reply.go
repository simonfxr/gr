package provider

import (
	"context"
	"errors"
	"fmt"

	bitbucket "github.com/ktrysmt/go-bitbucket"
)

// PrReplyComment posts a reply to an existing PR comment.
func (p Provider) PrReplyComment(ctx context.Context, nfo *Info, number, commentID int, body string) error {
	if nfo == nil {
		return errors.New("missing repo info")
	}
	switch p {
	case ProviderBitbucket:
		return prReplyCommentBitbucket(ctx, nfo, number, commentID, body)
	default:
		return fmt.Errorf("pr reply: %s backend not implemented yet", p)
	}
}

func prReplyCommentBitbucket(ctx context.Context, nfo *Info, number, commentID int, body string) error {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return err
	}
	parentID := commentID
	_, err = bb.Repositories.PullRequests.AddComment((&bitbucket.PullRequestCommentOptions{
		Owner:         nfo.Owner,
		RepoSlug:      nfo.Repo,
		PullRequestID: fmt.Sprintf("%d", number),
		Content:       body,
		Parent:        &parentID,
	}).WithContext(ctx))
	return err
}

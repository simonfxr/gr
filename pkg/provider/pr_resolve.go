package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/simonfxr/gr/pkg/auth"
	"github.com/simonfxr/gr/pkg/config"
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
		return fmt.Errorf("pr resolve: %s backend not implemented yet", p)
	}
}

func prResolveCommentBitbucket(ctx context.Context, nfo *Info, number, commentID int) error {
	if nfo.Variant == "self-hosted" {
		return errors.New("bitbucket server is not supported yet")
	}

	var cfg *config.ProviderConfig
	if nfo.Config != nil {
		cfg = &nfo.Config.Bitbucket
	}
	token, err := auth.GetToken(cfg, "BITBUCKET_TOKEN")
	if err != nil {
		return fmt.Errorf("bitbucket token: %w", err)
	}
	if token == "" {
		return errors.New("bitbucket token not configured")
	}
	user := auth.GetUsername(cfg, "BITBUCKET_USERNAME")
	if user == "" {
		return errors.New("bitbucket username (email) is required for comment resolution; set BITBUCKET_USERNAME or configure in ~/.config/gr/config.toml")
	}

	url := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests/%d/comments/%d/resolve",
		nfo.Owner, nfo.Repo, number, commentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("bitbucket API error %d: %s", resp.StatusCode, string(respBody))
}

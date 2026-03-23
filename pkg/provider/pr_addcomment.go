package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/simonfxr/gr/pkg/auth"
	"github.com/simonfxr/gr/pkg/config"
)

// AddCommentOptions holds parameters for adding an inline PR comment.
type AddCommentOptions struct {
	Path string
	Line int
	Body string
}

// PrAddComment adds an inline review comment to a PR.
func (p Provider) PrAddComment(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) error {
	if nfo == nil {
		return errors.New("missing repo info")
	}
	switch p {
	case ProviderBitbucket:
		return prAddCommentBitbucket(ctx, nfo, number, opts)
	case ProviderGitHub:
		return errors.New("pr addcomment: GitHub backend not implemented yet")
	case ProviderGitLab:
		return errors.New("pr addcomment: GitLab backend not implemented yet")
	default:
		return fmt.Errorf("unknown provider: %v", p)
	}
}

func prAddCommentBitbucket(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) error {
	if nfo.Provider == ProviderBitbucket && nfo.Variant == "self-hosted" {
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

	url := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests/%d/comments",
		nfo.Owner, nfo.Repo, number)

	payload := map[string]any{
		"content": map[string]any{"raw": opts.Body},
		"inline":  map[string]any{"to": opts.Line, "path": opts.Path},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/simonfxr/gr/pkg/auth"
	"github.com/simonfxr/gr/pkg/config"
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
	switch p {
	case ProviderBitbucket:
		return prAddCommentBitbucket(ctx, nfo, number, opts)
	case ProviderGitHub:
		return nil, errors.New("pr addcomment: GitHub backend not implemented yet")
	case ProviderGitLab:
		return nil, errors.New("pr addcomment: GitLab backend not implemented yet")
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

func prAddCommentBitbucket(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) (*PRComment, error) {
	if nfo.Provider == ProviderBitbucket && nfo.Variant == "self-hosted" {
		return nil, errors.New("bitbucket server is not supported yet")
	}

	var cfg *config.ProviderConfig
	if nfo.Config != nil {
		cfg = &nfo.Config.Bitbucket
	}
	token, err := auth.GetToken(cfg, "BITBUCKET_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("bitbucket token: %w", err)
	}
	if token == "" {
		return nil, errors.New("bitbucket token not configured")
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
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bitbucket API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID      int `json:"id"`
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
		Inline struct {
			To   int    `json:"to"`
			Path string `json:"path"`
		} `json:"inline"`
		User struct {
			DisplayName string `json:"display_name"`
		} `json:"user"`
		CreatedOn string `json:"created_on"`
		Links     struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Comment was created but we can't parse the response
		return &PRComment{Path: opts.Path, Line: opts.Line, Body: opts.Body}, nil
	}

	comment := &PRComment{
		ID:     result.ID,
		Author: result.User.DisplayName,
		Body:   result.Content.Raw,
		Path:   result.Inline.Path,
		Line:   result.Inline.To,
		URL:    result.Links.HTML.Href,
	}
	if t, err := time.Parse(time.RFC3339, result.CreatedOn); err == nil {
		comment.CreatedAt = t
	}
	return comment, nil
}

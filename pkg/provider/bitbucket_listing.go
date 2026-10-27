package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// bitbucketAPIClient talks to the Bitbucket Cloud REST API directly.
// Listings use explicit pagination: the SDK eagerly collects every page and
// cannot express all the query/partial-response parameters these endpoints use.
// BITBUCKET_API_BASE_URL overrides the API root (used by tests).
type bitbucketAPIClient struct {
	baseURL *url.URL
	http    *http.Client
	user    string
	token   string
}

func newBitbucketAPIClient(nfo *Info) (*bitbucketAPIClient, error) {
	user, token, err := bitbucketCredentials(nfo)
	if err != nil {
		return nil, err
	}
	base := os.Getenv("BITBUCKET_API_BASE_URL")
	if base == "" {
		base = "https://api.bitbucket.org/2.0"
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("bitbucket API URL: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid bitbucket API URL")
	}
	return &bitbucketAPIClient{
		baseURL: u, user: user, token: token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Never forward credentials through redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type bitbucketAPIError struct {
	status int
	body   string
}

func (e *bitbucketAPIError) Error() string {
	return fmt.Sprintf("bitbucket API: HTTP %d: %s", e.status, e.body)
}

// JSON string quoting also escapes quotes, backslashes and control characters
// in BBQL literals. URL encoding alone is not sufficient for query safety.
func bitbucketQueryString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func bitbucketListPages[T any](ctx context.Context, c *bitbucketAPIClient, endpoint string, params url.Values, visit func(T) bool) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	u = c.baseURL.ResolveReference(u)
	u.RawQuery = params.Encode()
	seen := make(map[string]bool)
	for {
		// Treat next as opaque, but do not send credentials to another origin.
		if u.Scheme != c.baseURL.Scheme || u.Host != c.baseURL.Host || u.User != nil {
			return fmt.Errorf("bitbucket listing: unsafe pagination URL")
		}
		if seen[u.String()] {
			return fmt.Errorf("bitbucket listing: repeated pagination URL")
		}
		seen[u.String()] = true
		var page struct {
			Next   string `json:"next"`
			Values []T    `json:"values"`
		}
		if err := c.get(ctx, u.String(), &page); err != nil {
			return err
		}
		for _, value := range page.Values {
			if !visit(value) {
				return nil
			}
		}
		if page.Next == "" {
			return nil
		}
		next, err := url.Parse(page.Next)
		if err != nil {
			return fmt.Errorf("bitbucket pagination URL: %w", err)
		}
		u = u.ResolveReference(next)
	}
}

// endpointURL resolves an already path-escaped endpoint against the API base.
func (c *bitbucketAPIClient) endpointURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	return c.baseURL.ResolveReference(u).String(), nil
}

func (c *bitbucketAPIClient) get(ctx context.Context, address string, result any) error {
	return c.do(ctx, http.MethodGet, address, nil, result)
}

// do sends a JSON request (payload may be nil) and decodes a 2xx JSON
// response into result (which may be nil).
func (c *bitbucketAPIClient) do(ctx context.Context, method, address string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &bitbucketAPIError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	if result == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode bitbucket response: %w", err)
	}
	return nil
}

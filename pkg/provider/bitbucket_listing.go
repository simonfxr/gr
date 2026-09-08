package provider

import (
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

// Listings use explicit pagination: the SDK eagerly collects every page and
// cannot express all the query/partial-response parameters these endpoints use.
type bitbucketListingClient struct {
	baseURL *url.URL
	http    *http.Client
	user    string
	token   string
}

func newBitbucketListingClient(nfo *Info) (*bitbucketListingClient, error) {
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
	return &bitbucketListingClient{
		baseURL: u, user: user, token: token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// Never forward credentials through redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type bitbucketListingError struct {
	status int
	body   string
}

func (e *bitbucketListingError) Error() string {
	return fmt.Sprintf("bitbucket listing: HTTP %d: %s", e.status, e.body)
}

// JSON string quoting also escapes quotes, backslashes and control characters
// in BBQL literals. URL encoding alone is not sufficient for query safety.
func bitbucketQueryString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func bitbucketListPages[T any](ctx context.Context, c *bitbucketListingClient, endpoint string, params url.Values, visit func(T) bool) error {
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

func (c *bitbucketListingClient) get(ctx context.Context, address string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &bitbucketListingError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode bitbucket listing: %w", err)
	}
	return nil
}

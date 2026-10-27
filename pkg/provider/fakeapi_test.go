package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// fakeAPI is an in-process stand-in for a provider REST API. Tests register
// handlers with ServeMux patterns such as "GET /api/v3/repos/owner/repo/pulls/{n}";
// an escaped path segment like "group%2Fproj" matches a single {wildcard}.
// Any request without a matching handler, or without the expected
// credentials, fails the test.
type fakeAPI struct {
	t    *testing.T
	mux  *http.ServeMux
	srv  *httptest.Server
	auth func(*http.Request) bool

	mu    sync.Mutex
	calls []string
}

func newFakeAPI(t *testing.T, auth func(*http.Request) bool) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, mux: http.NewServeMux(), auth: auth}
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		writeJSON(w, http.StatusNotFound, `{"message":"404 Not Found"}`)
	})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if f.auth != nil && !f.auth(r) {
			t.Errorf("%s %s: missing or wrong credentials", r.Method, r.URL)
			writeJSON(w, http.StatusUnauthorized, `{"message":"401 Unauthorized"}`)
			return
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// handle registers h for a ServeMux pattern.
func (f *fakeAPI) handle(pattern string, h http.HandlerFunc) { f.mux.HandleFunc(pattern, h) }

// reply registers a handler answering with a fixed status and JSON body.
func (f *fakeAPI) reply(pattern string, status int, body string) {
	f.handle(pattern, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status, body) })
}

// called reports whether a request "METHOD /path" was received.
func (f *fakeAPI) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// decodeJSON decodes a JSON request body into a generic map.
func decodeJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("%s %s: invalid JSON body: %v", r.Method, r.URL, err)
	}
	return m
}

// fakeGitHub serves a GitHub Enterprise style API (/api/v3) for owner/repo.
func fakeGitHub(t *testing.T) (*fakeAPI, *Info) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "gh-token")
	f := newFakeAPI(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer gh-token" })
	return f, &Info{Provider: ProviderGitHub, Variant: "self-hosted", HTTPBase: f.srv.URL, Owner: "owner", Repo: "repo"}
}

// fakeGitLab serves a self-hosted GitLab API (/api/v4) for the nested
// project group/sub/proj, which appears as group%2Fsub%2Fproj in paths.
func fakeGitLab(t *testing.T) (*fakeAPI, *Info) {
	t.Helper()
	t.Setenv("GITLAB_TOKEN", "gl-token")
	f := newFakeAPI(t, func(r *http.Request) bool { return r.Header.Get("Private-Token") == "gl-token" })
	return f, &Info{Provider: ProviderGitLab, Variant: "self-hosted", HTTPBase: f.srv.URL, Owner: "group/sub", Repo: "proj"}
}

// fakeBitbucketAPI serves the Bitbucket Cloud API for workspace/repo.
func fakeBitbucketAPI(t *testing.T) (*fakeAPI, *Info) {
	t.Helper()
	t.Setenv("BITBUCKET_TOKEN", "bb-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	f := newFakeAPI(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer bb-token" })
	t.Setenv("BITBUCKET_API_BASE_URL", f.srv.URL)
	return f, &Info{Provider: ProviderBitbucket, Owner: "workspace", Repo: "repo"}
}

// TestRequestsHonorContext checks that every provider aborts on a canceled
// context instead of sending (or retrying) the request.
func TestRequestsHonorContext(t *testing.T) {
	for _, setup := range []func(*testing.T) (*fakeAPI, *Info){fakeGitHub, fakeGitLab, fakeBitbucketAPI} {
		f, info := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := info.Provider.PrView(ctx, info, 1); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want context.Canceled", info.Provider, err)
		}
		f.mu.Lock()
		if len(f.calls) > 0 {
			t.Errorf("%s: sent %v despite canceled context", info.Provider, f.calls)
		}
		f.mu.Unlock()
	}
}

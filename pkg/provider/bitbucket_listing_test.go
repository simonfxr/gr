package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func listingTestServer(t *testing.T, handler http.HandlerFunc) *Info {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL+"/2.0")
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	return &Info{Provider: ProviderBitbucket, Owner: "workspace", Repo: "repo"}
}

func TestBitbucketPRRequestParameters(t *testing.T) {
	for _, tc := range []struct {
		state  string
		states []string
	}{
		{"", []string{"OPEN"}},
		{"open", []string{"OPEN"}},
		{" MERGED ", []string{"MERGED"}},
		{"closed", []string{"MERGED", "DECLINED", "SUPERSEDED"}},
		{"all", []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			requests := 0
			info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				q := r.URL.Query()
				if r.URL.Path != "/2.0/repositories/workspace/repo/pullrequests" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if !reflect.DeepEqual(q["state"], tc.states) {
					t.Errorf("states = %v, want %v", q["state"], tc.states)
				}
				if q.Get("sort") != "-created_on" || q.Get("pagelen") != "20" {
					t.Errorf("unexpected sort/pagelen: %v", q)
				}
				wantQuery := `destination.branch.name = "main" AND source.branch.name = "feature/\"quoted\\branch" AND author.uuid = "{abc}"`
				if q.Get("q") != wantQuery {
					t.Errorf("query = %q, want %q", q.Get("q"), wantQuery)
				}
				fields := q.Get("fields")
				for _, field := range []string{"next", "values.id", "values.author.nickname", "values.links.html.href"} {
					if !strings.Contains(fields, field) {
						t.Errorf("missing field %s in %s", field, fields)
					}
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing bearer authentication")
				}
				fmt.Fprint(w, `{"values":[]}`)
			})
			_, err := prListBitbucket(context.Background(), info, ListOptions{
				State: tc.state, Author: "{abc}", Base: "main", Head: "feature/\"quoted\\branch", Limit: 20,
			})
			if err != nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBitbucketPRLimitStopsBeforeNextPage(t *testing.T) {
	requests := 0
	info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("pagelen") != "10" {
			t.Error("small limits must respect minimum page length")
		}
		fmt.Fprint(w, `{"next":"?cursor=do-not-fetch","values":[
			{"id":9,"title":"Latest","state":"OPEN","created_on":"2026-09-08T12:00:00Z","author":{"nickname":"simon"},"links":{"html":{"href":"https://example.org/pr/9"}}},
			{"id":8},{"id":7}]}`)
	})
	prs, err := prListBitbucket(context.Background(), info, ListOptions{Limit: 2})
	if err != nil || len(prs) != 2 || requests != 1 {
		t.Fatalf("prs=%v requests=%d err=%v", prs, requests, err)
	}
	if prs[0].Number != 9 || prs[0].Title != "Latest" || prs[0].Author != "simon" ||
		prs[0].State != "open" || prs[0].CreatedAt.IsZero() || prs[0].URL != "https://example.org/pr/9" {
		t.Fatalf("incorrect PR conversion: %+v", prs[0])
	}
}

func TestBitbucketPRNameFilterPaginatesUntilEnoughExactMatches(t *testing.T) {
	requests := 0
	info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if r.URL.Query().Get("q") != `(author.nickname ~ "Simon" OR author.display_name ~ "Simon")` {
				t.Errorf("unexpected query: %v", r.URL.Query())
			}
			fmt.Fprint(w, `{"next":"?cursor=opaque-token","values":[
				{"id":5,"author":{"nickname":"Simon-other"}},
				{"id":4,"author":{"nickname":"sIMON"}}]}`)
		} else {
			if r.URL.RawQuery != "cursor=opaque-token" {
				t.Errorf("next URL was not followed verbatim: %s", r.URL)
			}
			fmt.Fprint(w, `{"next":"?cursor=unneeded","values":[
				{"id":3,"author":{"nickname":"other","display_name":"SIMON"}},
				{"id":2,"author":{"nickname":"Simon"}}]}`)
		}
	})
	prs, err := prListBitbucket(context.Background(), info, ListOptions{Author: "Simon", Limit: 2})
	if err != nil || requests != 2 || len(prs) != 2 {
		t.Fatalf("prs=%v requests=%d err=%v", prs, requests, err)
	}
	if prs[0].Number != 4 || prs[1].Number != 3 {
		t.Fatalf("wrong matching results: %v", prs)
	}
}

func TestBitbucketPRNameFilterFallback(t *testing.T) {
	requests := 0
	info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "unsupported author.display_name", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		if q.Get("q") != `destination.branch.name = "main"` || q.Get("state") != "MERGED" || q.Get("pagelen") != "50" {
			t.Errorf("fallback lost filters: %v", q)
		}
		fmt.Fprint(w, `{"values":[{"id":1,"author":{"display_name":"Other"}},{"id":2,"author":{"display_name":"Simon Reiser"}}]}`)
	})
	prs, err := prListBitbucket(context.Background(), info, ListOptions{
		Author: "simon reiser", Base: "main", State: "merged", Limit: 1,
	})
	if err != nil || requests != 2 || len(prs) != 1 || prs[0].Number != 2 {
		t.Fatalf("prs=%v requests=%d err=%v", prs, requests, err)
	}
}

func TestBitbucketPRPageSizeCap(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 30, 50, 51, 100, 101} {
		for _, author := range []string{"", "Simon Reiser"} {
			t.Run(fmt.Sprintf("limit=%d/author=%s", limit, author), func(t *testing.T) {
				requests := 0
				info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
					requests++
					q := r.URL.Query()
					pageSize, err := strconv.Atoi(q.Get("pagelen"))
					// Model the PR endpoint's actual limit, not the generic
					// Bitbucket pagination limit of 100.
					if err != nil || pageSize < 10 || pageSize > 50 {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"type":"error","error":{"message":"Invalid pagelen"}}`)
						return
					}
					if q.Get("q") != "" {
						http.Error(w, "unsupported author.display_name", http.StatusBadRequest)
						return
					}
					if !reflect.DeepEqual(q["state"], []string{"MERGED", "DECLINED", "SUPERSEDED"}) {
						t.Errorf("lost closed-state filter: %v", q)
					}
					fmt.Fprint(w, `{"values":[{"id":1,"state":"MERGED","author":{"display_name":"Simon Reiser"}}]}`)
				})
				prs, err := prListBitbucket(context.Background(), info, ListOptions{
					Author: author, State: "closed", Limit: limit,
				})
				wantRequests := 1
				if author != "" {
					wantRequests = 2
				}
				if err != nil || len(prs) != 1 || requests != wantRequests {
					t.Fatalf("prs=%v requests=%d err=%v", prs, requests, err)
				}
			})
		}
	}
}

func TestBitbucketPRAccountIDAndUnlimitedPagination(t *testing.T) {
	for _, limit := range []int{0, -1, 101} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			requests := 0
			info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					q := r.URL.Query()
					if q.Get("pagelen") != "50" || q.Get("q") != `author.account_id = "557058:abc"` {
						t.Errorf("unexpected query %v", q)
					}
					fmt.Fprint(w, `{"next":"?cursor=2","values":[]}`)
				} else {
					fmt.Fprint(w, `{"values":[{"id":1,"author":{"display_name":"Display name"}}]}`)
				}
			})
			prs, err := prListBitbucket(context.Background(), info, ListOptions{Author: "557058:abc", Limit: limit})
			if err != nil || requests != 2 || len(prs) != 1 || prs[0].Author != "Display name" {
				t.Fatalf("prs=%v requests=%d err=%v", prs, requests, err)
			}
		})
	}
}

func TestBitbucketListingErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.Error(w, "failure", status)
			})
			_, err := prListBitbucket(context.Background(), info, ListOptions{Author: "Simon"})
			var apiErr *bitbucketAPIError
			if !errors.As(err, &apiErr) || apiErr.status != status || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBitbucketListingBasicAuthAndCancellation(t *testing.T) {
	info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "test-user" || password != "test-token" {
			t.Error("missing basic authentication")
		}
		fmt.Fprint(w, `{"values":[]}`)
	})
	t.Setenv("BITBUCKET_USERNAME", "test-user")
	if _, err := prListBitbucket(context.Background(), info, ListOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prListBitbucket(ctx, info, ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestBitbucketListingRejectsUnsafeOrRepeatedNext(t *testing.T) {
	for _, next := range []string{"https://other.example/steal", "?loop=1"} {
		t.Run(next, func(t *testing.T) {
			requests := 0
			info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				json.NewEncoder(w).Encode(map[string]any{"next": next, "values": []any{}})
			})
			_, err := prListBitbucket(context.Background(), info, ListOptions{})
			if err == nil || requests > 2 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBitbucketListingRejectsRedirectAndMalformedJSON(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			requests := 0
			info := listingTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if redirect {
					http.Redirect(w, r, "/other", http.StatusFound)
				} else {
					fmt.Fprint(w, `{"values":`)
				}
			})
			_, err := prListBitbucket(context.Background(), info, ListOptions{})
			if err == nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBitbucketQueryString(t *testing.T) {
	value := "\" OR state = \"OPEN\"\\\n&é"
	literal := bitbucketQueryString(value)
	var decoded string
	if err := json.Unmarshal([]byte(literal), &decoded); err != nil || decoded != value {
		t.Fatalf("invalid query literal %q: %v", literal, err)
	}
	q := url.Values{"q": {"title = " + literal}}
	decodedQuery, err := url.ParseQuery(q.Encode())
	if err != nil || decodedQuery.Get("q") != "title = "+literal {
		t.Fatalf("query URL encoding failed: %v", err)
	}
}

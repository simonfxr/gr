package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBitbucketBranchNameQuery(t *testing.T) {
	tests := []struct {
		pattern string
		query   string
	}{
		{"", ""},
		{"  main  ", `name = "main"`},
		{`literal\*\?\[x\]`, `name = "literal*?[x]"`},
		{`foo\\bar`, `name = "foo\\bar"`},
		{`a"b`, `name = "a\"b"`},
		{"feature/*", `name ~ "feature/"`},
		{"*needle*", `name ~ "needle"`},
		{"a?longer", `name ~ "longer"`},
		{"[ab]suffix", `name ~ "suffix"`},
		{`[\]]suffix`, `name ~ "suffix"`},
		{"[^a-z]*", ""},
		{"*?", ""},
		{"bad[", ""},
		{"bad\\", ""},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			if got := bitbucketBranchNameQuery(tt.pattern); got != tt.query {
				t.Fatalf("query = %q, want %q", got, tt.query)
			}
		})
	}
}

func TestBitbucketBranchListPaginationAndLocalFilters(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got, want := r.URL.EscapedPath(), "/2.0/repositories/work%20space/repo%2Fname/refs/branches"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		q := r.URL.Query()
		if requests == 1 {
			if q.Get("pagelen") != "100" || q.Get("fields") != "next,values.name,values.target.date,values.target.author.user.display_name" {
				t.Errorf("unexpected projection/pagination: %v", q)
			}
			if got := q.Get("q"); !strings.HasPrefix(got, `name ~ "feature/" AND target.author.user.display_name ~ "alice" AND target.date > `) {
				t.Errorf("query = %q", got)
			}
			_, cutoffText, _ := strings.Cut(q.Get("q"), "target.date > ")
			cutoff, err := time.Parse(time.RFC3339, cutoffText)
			if err != nil || now.Add(-48*time.Hour).Sub(cutoff).Abs() > 5*time.Second {
				t.Errorf("unexpected date cutoff: %q", cutoffText)
			}
			if q.Has("sort") {
				t.Error("sort must remain local")
			}
			fmt.Fprintf(w, `{"next":%q,"values":[
				{"name":"feature/Z","target":{"date":%q,"author":{"user":{"display_name":"ALICE"}}}},
				{"name":"feature/old","target":{"date":%q,"author":{"user":{"display_name":"Alice"}}}},
				{"name":"feature/wrong-author","target":{"date":%q,"author":{"user":{"display_name":"Bob"}}}}
			]}`, server.URL+"/2.0/repositories/work%20space/repo%2Fname/refs/branches?cursor=next", now.Add(-time.Hour).Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
			return
		}
		if q.Get("cursor") != "next" {
			t.Errorf("pagination did not follow next URL: %v", q)
		}
		fmt.Fprintf(w, `{"values":[
			{"name":"feature/a","target":{"date":%q,"author":{"user":{"display_name":"Alice Smith"}}}},
			{"name":"feature/a/nested","target":{"date":%q,"author":{"user":{"display_name":"Alice"}}}},
			{"name":"feature/invalid-date","target":{"date":"not-a-date","author":{"user":{"display_name":"Alice"}}}},
			{"name":"feature/missing-target"}
		]}`, now.Format(time.RFC3339), now.Format(time.RFC3339))
	}))
	defer server.Close()
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL+"/2.0")
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	got, err := branchListRemoteBitbucket(context.Background(), &Info{Owner: "work space", Repo: "repo/name"}, BranchListOptions{
		Pattern: " feature/* ", Author: " alice ", Since: "2d", Sort: "date",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []BranchInfo{
		{Name: "feature/a", CommitDate: now, Author: "Alice Smith"},
		{Name: "feature/Z", CommitDate: now.Add(-time.Hour), Author: "ALICE"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("branches = %#v, want %#v", got, want)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestBitbucketBranchListNestedFilterFallback(t *testing.T) {
	for _, pattern := range []string{"feature/*", ""} {
		t.Run(pattern, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				query := r.URL.Query().Get("q")
				if requests == 1 {
					if !strings.Contains(query, `target.author.user.display_name ~ "Alice"`) || !strings.Contains(query, "target.date > ") {
						t.Errorf("missing nested filters: %q", query)
					}
					http.Error(w, "unsupported query field", http.StatusBadRequest)
					return
				}
				if query != bitbucketBranchNameQuery(pattern) {
					t.Errorf("fallback query = %q", query)
				}
				fmt.Fprintf(w, `{"values":[
					{"name":"feature/new","target":{"date":%q,"author":{"user":{"display_name":"ALICE"}}}},
					{"name":"feature/old","target":{"date":"2000-01-01T00:00:00Z","author":{"user":{"display_name":"Alice"}}}},
					{"name":"feature/other","target":{"date":%q,"author":{"user":{"display_name":"Bob"}}}}
				]}`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
			}))
			defer server.Close()
			t.Setenv("BITBUCKET_API_BASE_URL", server.URL+"/2.0")
			t.Setenv("BITBUCKET_TOKEN", "test-token")
			t.Setenv("BITBUCKET_USERNAME", "")
			got, err := branchListRemoteBitbucket(context.Background(), &Info{Owner: "workspace", Repo: "repo"}, BranchListOptions{
				Pattern: pattern, Author: "Alice", Since: "1w",
			})
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || len(got) != 1 || got[0].Name != "feature/new" {
				t.Fatalf("requests = %d, branches = %#v", requests, got)
			}
		})
	}
}

func TestBitbucketBranchListGlobs(t *testing.T) {
	names := []string{"Feature/x", "feature/a", "feature/b", "feature/a/nested", "feature/*", "a]suffix", "]suffix", "main", "mainline"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately return a superset: local path.Match must still enforce
		// case sensitivity, slash boundaries, classes, and escaped literals.
		values := make([]map[string]string, 0, len(names))
		for _, name := range names {
			values = append(values, map[string]string{"name": name})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"values": values}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL+"/2.0/")
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	for _, pattern := range []string{"feature/*", "feature/[ab]", `feature/\*`, "*suffix", `[\]]suffix`, "main", "bad[", "*"} {
		t.Run(pattern, func(t *testing.T) {
			got, err := branchListRemoteBitbucket(context.Background(), &Info{Owner: "workspace", Repo: "repo"}, BranchListOptions{Pattern: pattern})
			if err != nil {
				t.Fatal(err)
			}
			want := make([]BranchInfo, 0)
			for _, name := range names {
				if match, _ := path.Match(pattern, name); match {
					want = append(want, BranchInfo{Name: name})
				}
			}
			want = sortBranches(want, BranchListOptions{})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("branches = %#v, want %#v", got, want)
			}
		})
	}
}

func FuzzBitbucketBranchNameQuery(f *testing.F) {
	for _, seed := range [][2]string{
		{"feature/*", "feature/new"}, {`feature/\*`, "feature/*"},
		{`[\]]suffix`, "]suffix"}, {"main", "main"}, {"bad\\", ""},
		{"a[^x-z]bc*", "aQbcend"}, {"", ""}, {"??", "éé"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		// Branch names arrive as JSON strings, so invalid UTF-8 byte sequences
		// cannot occur in the decoded API response.
		if !utf8.ValidString(name) {
			t.Skip()
		}
		query := bitbucketBranchNameQuery(pattern)
		matches, _ := path.Match(strings.TrimSpace(pattern), name)
		if !matches || query == "" {
			return
		}
		parts := strings.SplitN(query, " ", 3)
		var literal string
		if err := json.Unmarshal([]byte(parts[2]), &literal); err != nil {
			t.Fatal(err)
		}
		if parts[1] == "=" && literal != name || parts[1] == "~" && !strings.Contains(name, literal) {
			t.Fatalf("query %s excludes glob %q match %q", query, pattern, name)
		}
	})
}

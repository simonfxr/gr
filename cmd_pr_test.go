package main

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/simonfxr/gr/pkg/provider"
)

func TestPRCommentsResolutionOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") != "/repositories/workspace/repo/pullrequests/42/comments" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"values":[
			{"id":1,"content":{"raw":"Resolved comment"},"resolution":{"type":"comment_resolution","created_on":"2026-10-13T10:00:00Z"}},
			{"id":2,"content":{"raw":"Unresolved comment"},"resolution":null},
			{"id":3,"content":{"raw":"No resolution field"}},
			{"id":4,"content":{"raw":"Deleted comment"},"deleted":true,"resolution":{"type":"comment_resolution"}}
		]}`)
	}))
	defer server.Close()
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL)
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	info := &provider.Info{Provider: provider.ProviderBitbucket, Owner: "workspace", Repo: "repo"}

	for _, jsonOutput := range []bool{true, false} {
		name := "table"
		if jsonOutput {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			output, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			originalStdout := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = originalStdout }()

			if err := runPR(t.Context(), &PRCmd{Comments: &PRCommentsCmd{Number: "42", JSON: jsonOutput}}, info); err != nil {
				t.Fatal(err)
			}
			if _, err := output.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(output)
			if err != nil {
				t.Fatal(err)
			}
			if jsonOutput {
				var comments []map[string]any
				if err := json.Unmarshal(data, &comments); err != nil {
					t.Fatalf("invalid JSON output: %v\n%s", err, data)
				}
				if len(comments) != 3 {
					t.Fatalf("got %d comments, want 3", len(comments))
				}
				for i, want := range []bool{true, false, false} {
					if got, ok := comments[i]["resolved"].(bool); !ok || got != want {
						t.Errorf("comment %d resolved = %v, want explicit boolean %v", i+1, comments[i]["resolved"], want)
					}
				}
				return
			}
			// Redirected table output uses CSV, with the same headers and row values.
			rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			want := [][]string{
				{"ID", "When", "Author", "Resolved", "Comment"},
				{"1", "", "", "true", "Resolved comment"},
				{"2", "", "", "false", "Unresolved comment"},
				{"3", "", "", "false", "No resolution field"},
			}
			if !reflect.DeepEqual(rows, want) {
				t.Errorf("table rows = %v, want %v", rows, want)
			}
		})
	}
}

func TestFormatState(t *testing.T) {
	for _, tc := range []struct {
		state, providerState string
		draft                bool
		want                 string
	}{
		{"open", "opened", false, "open"},
		{"open", "open", true, "open, draft"},
		{"merged", "closed", false, "merged"}, // GitHub
		{"closed", "declined", false, "closed (declined)"},
		{"open", "locked", false, "open (locked)"},
	} {
		if got := formatState(tc.state, tc.providerState, tc.draft); got != tc.want {
			t.Errorf("formatState(%q, %q, %v) = %q, want %q", tc.state, tc.providerState, tc.draft, got, tc.want)
		}
	}
}

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

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	output, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	originalStdout := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = originalStdout }()
	f()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPRTasksOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repositories/workspace/repo/pullrequests/42/tasks" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("q") {
			t.Errorf("unexpected filter for --state all: %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"values":[
			{"id":1,"state":"RESOLVED","content":{"raw":"clean up useJournalMedia"},"creator":{"nickname":"mathias"}},
			{"id":2,"state":"UNRESOLVED","pending":true,"content":{"raw":"Fix UI"},"comment":{"id":77}}
		]}`)
	}))
	defer server.Close()
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL)
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	info := &provider.Info{Provider: provider.ProviderBitbucket, Owner: "workspace", Repo: "repo"}

	t.Run("json", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := runPR(&PRCmd{Tasks: &PRTasksCmd{Number: "42", State: "all", JSON: true}}, info); err != nil {
				t.Fatal(err)
			}
		})
		var tasks []map[string]any
		if err := json.Unmarshal([]byte(out), &tasks); err != nil {
			t.Fatalf("invalid JSON output: %v\n%s", err, out)
		}
		if len(tasks) != 2 || tasks[0]["resolved"] != true || tasks[1]["pending"] != true || tasks[1]["comment_id"] != float64(77) {
			t.Errorf("unexpected tasks: %v", tasks)
		}
	})
	t.Run("table", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := runPR(&PRCmd{Tasks: &PRTasksCmd{Number: "42", State: "all"}}, info); err != nil {
				t.Fatal(err)
			}
		})
		rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{
			{"ID", "Resolved", "Pending", "Creator", "When", "Comment", "Task"},
			{"1", "true", "false", "mathias", "", "", "clean up useJournalMedia"},
			{"2", "false", "true", "", "", "77", "Fix UI"},
		}
		if !reflect.DeepEqual(rows, want) {
			t.Errorf("table rows = %v, want %v", rows, want)
		}
	})
}

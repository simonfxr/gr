package provider

import (
	"net/http"
	"testing"
	"time"
)

const bitbucketPR7 = `{"id":7,"title":"Add z","description":"desc","state":"OPEN","draft":true,
	"author":{"nickname":"carol","display_name":"Carol"},
	"created_on":"2026-10-01T10:00:00.000000+00:00","updated_on":"2026-10-02T10:00:00.000000+00:00",
	"source":{"branch":{"name":"feature/z"}},"destination":{"branch":{"name":"main"}},
	"links":{"html":{"href":"https://bitbucket.org/workspace/repo/pull-requests/7"}}}`

func TestBitbucketPrView(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("GET /repositories/workspace/repo/pullrequests/7", http.StatusOK, bitbucketPR7)
	got, err := ProviderBitbucket.PrView(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 || got.Title != "Add z" || got.Body != "desc" || got.Author != "carol" ||
		got.Base != "main" || got.Head != "feature/z" || got.Merged ||
		!got.CreatedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) ||
		got.URL != "https://bitbucket.org/workspace/repo/pull-requests/7" {
		t.Errorf("got %+v", got)
	}
}

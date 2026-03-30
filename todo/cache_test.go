package todo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// mockTransport lets tests intercept HTTP calls made by api.RESTClient.
type mockTransport struct {
	handler http.HandlerFunc
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	m.handler(w, req)
	return w.Result(), nil
}

func TestIssueCache_StateByNumber(t *testing.T) {
	c := NewIssueCache()
	c.Add(1, "Fix bug", "open")
	c.Add(2, "Old task", "closed")

	state, found := c.StateByNumber(1)
	if !found || state != "open" {
		t.Errorf("StateByNumber(1) = %q, %v; want %q, true", state, found, "open")
	}

	state, found = c.StateByNumber(2)
	if !found || state != "closed" {
		t.Errorf("StateByNumber(2) = %q, %v; want %q, true", state, found, "closed")
	}

	_, found = c.StateByNumber(99)
	if found {
		t.Error("StateByNumber(99): want not found")
	}
}

func TestIssueCache_NumberByTitle(t *testing.T) {
	c := NewIssueCache()
	c.Add(1, "Fix bug", "open")
	c.Add(2, "Old task", "closed") // closed: should NOT appear in byTitle

	n, found := c.NumberByTitle("Fix bug")
	if !found || n != 1 {
		t.Errorf("NumberByTitle(Fix bug) = %d, %v; want 1, true", n, found)
	}

	// Case-insensitive lookup
	n, found = c.NumberByTitle("fix bug")
	if !found || n != 1 {
		t.Errorf("NumberByTitle(fix bug) = %d, %v; want 1, true", n, found)
	}

	// Closed issue not reachable by title
	_, found = c.NumberByTitle("Old task")
	if found {
		t.Error("NumberByTitle(Old task): closed issue should not be in byTitle")
	}
}

func TestIssueCache_Add_UpdatesExisting(t *testing.T) {
	c := NewIssueCache()
	c.Add(1, "Fix bug", "open")
	c.Add(1, "Fix bug", "closed") // re-add as closed (simulates post-close state)

	state, found := c.StateByNumber(1)
	if !found || state != "closed" {
		t.Errorf("StateByNumber(1) after re-add = %q, %v; want %q, true", state, found, "closed")
	}
}

func TestFetchIssueCache_PopulatesMaps(t *testing.T) {
	calls := 0
	tr := &mockTransport{func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			// page 1: one open issue, one closed issue, one PR (should be filtered)
			fmt.Fprint(w, `[
				{"number":1,"title":"Fix bug","state":"open"},
				{"number":2,"title":"Old task","state":"closed"},
				{"number":3,"title":"A PR","state":"open","pull_request":{"url":"https://api.github.com/repos/o/r/pulls/3"}}
			]`)
		default:
			// page 2: empty → stop
			fmt.Fprint(w, `[]`)
		}
	}}

	client, err := api.NewRESTClient(api.ClientOptions{Transport: tr})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	repo := repository.Repository{Owner: "owner", Name: "repo"}

	cache, err := FetchIssueCache(context.Background(), client, repo)
	if err != nil {
		t.Fatalf("FetchIssueCache: %v", err)
	}

	// open issue in both maps
	if state, found := cache.StateByNumber(1); !found || state != "open" {
		t.Errorf("StateByNumber(1) = %q, %v; want open, true", state, found)
	}
	if n, found := cache.NumberByTitle("fix bug"); !found || n != 1 {
		t.Errorf("NumberByTitle(fix bug) = %d, %v; want 1, true", n, found)
	}

	// closed issue in byNumber only
	if state, found := cache.StateByNumber(2); !found || state != "closed" {
		t.Errorf("StateByNumber(2) = %q, %v; want closed, true", state, found)
	}
	if _, found := cache.NumberByTitle("old task"); found {
		t.Error("NumberByTitle(old task): closed issue should not be in byTitle")
	}

	// PR filtered out
	if _, found := cache.StateByNumber(3); found {
		t.Error("StateByNumber(3): PR should be filtered")
	}

	if calls != 2 {
		t.Errorf("HTTP calls = %d; want 2", calls)
	}
}

func TestFetchIssueCache_Paginates(t *testing.T) {
	// Verify pagination: 3 pages of results + 1 empty terminator = 4 calls.
	page := 0
	tr := &mockTransport{func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		if page <= 3 {
			item := fmt.Sprintf(`[{"number":%d,"title":"Task %d","state":"open"}]`, page, page)
			fmt.Fprint(w, item)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}}

	client, _ := api.NewRESTClient(api.ClientOptions{Transport: tr})
	repo := repository.Repository{Owner: "o", Name: "r"}

	cache, err := FetchIssueCache(context.Background(), client, repo)
	if err != nil {
		t.Fatalf("FetchIssueCache: %v", err)
	}
	if page != 4 {
		t.Errorf("HTTP calls = %d; want 4", page)
	}
	for i := 1; i <= 3; i++ {
		if _, found := cache.StateByNumber(i); !found {
			t.Errorf("StateByNumber(%d): want found", i)
		}
	}
}

package todo

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

var testRepo = repository.Repository{Owner: "owner", Name: "repo"}

// mockClient returns an api.RESTClient that serves HTTP via the given handler.
func mockClient(t *testing.T, handler http.HandlerFunc) *api.RESTClient {
	t.Helper()
	client, err := api.NewRESTClient(api.ClientOptions{
		Transport: &mockTransport{handler},
	})
	if err != nil {
		t.Fatalf("creating mock client: %v", err)
	}
	return client
}

func TestProcessItem_CloseByNum_AlreadyClosed(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(5, "Fix login", "closed")

	item := Item{
		LineIndex: 0, Raw: "- [x] Fix login (#5)", Action: ActionCloseByNum,
		Prefix: "- [x] ", Task: "Fix login", IssueNum: 5,
	}

	// client is nil — no HTTP calls should be made for an already-closed issue
	result := ProcessItem(context.Background(), nil, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "skip" {
		t.Errorf("Verb = %q; want skip", result.Verb)
	}
	if result.Err != nil {
		t.Errorf("unexpected error: %v", result.Err)
	}
}

func TestProcessItem_CloseByNum_NotFound(t *testing.T) {
	cache := NewIssueCache() // empty cache — issue not found

	item := Item{
		LineIndex: 0, Raw: "- [x] Fix login (#5)", Action: ActionCloseByNum,
		Prefix: "- [x] ", Task: "Fix login", IssueNum: 5,
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "skip" {
		t.Errorf("Verb = %q; want skip", result.Verb)
	}
}

func TestProcessItem_CloseByNum_ClosesOpenIssue(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(5, "Fix login", "open")

	patchCalled := false
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/5") {
			patchCalled = true
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"number":5,"state":"closed"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	item := Item{
		LineIndex: 0, Raw: "- [x] Fix login (#5)", Action: ActionCloseByNum,
		Prefix: "- [x] ", Task: "Fix login", IssueNum: 5,
	}

	result := ProcessItem(context.Background(), client, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "closed" {
		t.Errorf("Verb = %q; want closed", result.Verb)
	}
	if !patchCalled {
		t.Error("expected PATCH /issues/5 to be called")
	}
	if result.Err != nil {
		t.Errorf("unexpected error: %v", result.Err)
	}
}

func TestProcessItem_CloseByNum_DryRun(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(7, "Refactor auth", "open")

	item := Item{
		LineIndex: 0, Raw: "- [x] Refactor auth (#7)", Action: ActionCloseByNum,
		Prefix: "- [x] ", Task: "Refactor auth", IssueNum: 7,
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, true, "TODO.md")

	if result.Verb != "would-close" {
		t.Errorf("Verb = %q; want would-close", result.Verb)
	}
}

func TestProcessItem_CreateIssue_AlreadyExists(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(10, "Add dark mode", "open")

	item := Item{
		LineIndex: 1, Raw: "- [ ] Add dark mode", Action: ActionCreateIssue,
		Prefix: "- [ ] ", Task: "Add dark mode",
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "skip" {
		t.Errorf("Verb = %q; want skip", result.Verb)
	}
	if result.IssueNum != 10 {
		t.Errorf("IssueNum = %d; want 10", result.IssueNum)
	}
	want := "- [ ] Add dark mode (#10)"
	if result.NewLine != want {
		t.Errorf("NewLine = %q; want %q", result.NewLine, want)
	}
}

func TestProcessItem_CreateIssue_CreatesNew(t *testing.T) {
	cache := NewIssueCache() // no existing issue

	postCalled := false
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/issues") {
			postCalled = true
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"number":42,"state":"open"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	item := Item{
		LineIndex: 2, Raw: "- [ ] Add dark mode", Action: ActionCreateIssue,
		Prefix: "- [ ] ", Task: "Add dark mode",
	}

	result := ProcessItem(context.Background(), client, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "created" {
		t.Errorf("Verb = %q; want created", result.Verb)
	}
	if result.IssueNum != 42 {
		t.Errorf("IssueNum = %d; want 42", result.IssueNum)
	}
	if !postCalled {
		t.Error("expected POST /issues to be called")
	}
	// After creation, issue should be in cache
	if _, found := cache.NumberByTitle("add dark mode"); !found {
		t.Error("newly created issue should be added to cache")
	}
}

func TestProcessItem_CreateIssue_DryRun(t *testing.T) {
	cache := NewIssueCache()

	item := Item{
		LineIndex: 3, Raw: "- [ ] Add dark mode", Action: ActionCreateIssue,
		Prefix: "- [ ] ", Task: "Add dark mode",
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, true, "TODO.md")

	if result.Verb != "would-create" {
		t.Errorf("Verb = %q; want would-create", result.Verb)
	}
}

func TestProcessItem_CloseByTitle_Found(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(8, "Remove deprecated API", "open")

	patchCalled := false
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/8") {
			patchCalled = true
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"number":8,"state":"closed"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	item := Item{
		LineIndex: 0, Raw: "- [x] Remove deprecated API", Action: ActionCloseByTitle,
		Prefix: "- [x] ", Task: "Remove deprecated API",
	}

	result := ProcessItem(context.Background(), client, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "closed" {
		t.Errorf("Verb = %q; want closed", result.Verb)
	}
	if result.IssueNum != 8 {
		t.Errorf("IssueNum = %d; want 8", result.IssueNum)
	}
	if !patchCalled {
		t.Error("expected PATCH /issues/8 to be called")
	}
}

func TestProcessItem_CloseByTitle_NotFound(t *testing.T) {
	cache := NewIssueCache() // no matching issue

	item := Item{
		LineIndex: 0, Raw: "- [x] Remove deprecated API", Action: ActionCloseByTitle,
		Prefix: "- [x] ", Task: "Remove deprecated API",
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, false, "TODO.md")

	if result.Verb != "skip" {
		t.Errorf("Verb = %q; want skip", result.Verb)
	}
}

func TestProcessItem_CloseByTitle_DryRun(t *testing.T) {
	cache := NewIssueCache()
	cache.Add(8, "Remove deprecated API", "open")

	item := Item{
		LineIndex: 0, Raw: "- [x] Remove deprecated API", Action: ActionCloseByTitle,
		Prefix: "- [x] ", Task: "Remove deprecated API",
	}

	result := ProcessItem(context.Background(), nil, testRepo, item, cache, true, "TODO.md")

	if result.Verb != "would-close" {
		t.Errorf("Verb = %q; want would-close", result.Verb)
	}
	if result.IssueNum != 8 {
		t.Errorf("IssueNum = %d; want 8", result.IssueNum)
	}
}

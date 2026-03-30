# Bulk Issue Prefetch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-item GitHub API read calls with a single paginated prefetch of all repository issues, reducing API usage from O(N items) to O(N issues / 100).

**Architecture:** Before parallel processing, `FetchIssueCache` walks `GET /repos/{owner}/{repo}/issues?state=all&per_page=100` to build an `IssueCache` (two maps: number→state, lowercase-title→number). `ProcessItem` goroutines read from this immutable snapshot; only write calls (create/close) still hit the API. A `sync.RWMutex` guards the one post-create write.

**Tech Stack:** Go 1.25, `github.com/cli/go-gh/v2/pkg/api`, `encoding/json`, `net/http` (stdlib test transport), `sync`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `todo/cache.go` | Create | `IssueCache` struct, methods, `FetchIssueCache` |
| `todo/cache_test.go` | Create | Tests for `IssueCache` methods and `FetchIssueCache` |
| `todo/processor.go` | Modify | Remove `findOpenIssue`, `getIssueState`; update `ProcessItem` signature and cases |
| `todo/processor_test.go` | Create | Tests for updated `ProcessItem` |
| `todo/runner.go` | Modify | Add `*IssueCache` parameter to `Run` |
| `main.go` | Modify | Call `FetchIssueCache` before `todo.Run` |

---

## Task 1: IssueCache struct and methods

**Files:**
- Create: `todo/cache.go`
- Create: `todo/cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `todo/cache_test.go`:

```go
package todo

import (
	"testing"
)

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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./todo/... -run TestIssueCache -v
```

Expected: compile error — `NewIssueCache`, `IssueCache` not defined.

- [ ] **Step 3: Implement IssueCache**

Create `todo/cache.go`:

```go
package todo

import (
	"strings"
	"sync"
)

// IssueCache is a read-optimised snapshot of all repository issues.
// It is safe for concurrent use; Add uses a full write lock, reads use a read lock.
type IssueCache struct {
	mu       sync.RWMutex
	byNumber map[int]string // issue# → "open" | "closed"
	byTitle  map[string]int // lowercase(title) → issue# (open issues only)
}

// NewIssueCache returns an initialised, empty IssueCache.
func NewIssueCache() *IssueCache {
	return &IssueCache{
		byNumber: make(map[int]string),
		byTitle:  make(map[string]int),
	}
}

// StateByNumber returns the state ("open" or "closed") for issue n.
func (c *IssueCache) StateByNumber(n int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.byNumber[n]
	return s, ok
}

// NumberByTitle returns the issue number for an open issue with the given title
// (case-insensitive). Only open issues are indexed by title.
func (c *IssueCache) NumberByTitle(title string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.byTitle[strings.ToLower(title)]
	return n, ok
}

// Add inserts or updates an issue in the cache.
// Open issues are indexed by title; closed issues are not.
func (c *IssueCache) Add(n int, title, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byNumber[n] = state
	if state == "open" {
		c.byTitle[strings.ToLower(title)] = n
	}
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./todo/... -run TestIssueCache -v
```

Expected: all three tests PASS.

- [ ] **Step 5: Commit**

```bash
git add todo/cache.go todo/cache_test.go
git commit -m "feat: add IssueCache struct with RWMutex-protected maps"
```

---

## Task 2: FetchIssueCache

**Files:**
- Modify: `todo/cache.go` (add `FetchIssueCache` and `issueListItem`)
- Modify: `todo/cache_test.go` (add `TestFetchIssueCache_*`)

- [ ] **Step 1: Write the failing tests**

Append to `todo/cache_test.go`:

```go
import (
	"context"
	"encoding/json"
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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./todo/... -run TestFetchIssueCache -v
```

Expected: compile error — `FetchIssueCache` not defined.

- [ ] **Step 3: Implement FetchIssueCache**

Add to `todo/cache.go` (append after the `Add` method):

```go
import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// issueListItem is the subset of fields used from GET /repos/{owner}/{repo}/issues.
type issueListItem struct {
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	State       string           `json:"state"`
	PullRequest *json.RawMessage `json:"pull_request"` // non-nil means this is a PR
}

// FetchIssueCache paginates GET /repos/{owner}/{repo}/issues?state=all&per_page=100
// and returns a populated IssueCache. Pull requests are filtered out.
func FetchIssueCache(ctx context.Context, client *api.RESTClient, repo repository.Repository) (*IssueCache, error) {
	cache := NewIssueCache()
	for page := 1; ; page++ {
		var items []issueListItem
		path := fmt.Sprintf("repos/%s/%s/issues?state=all&per_page=100&page=%d", repo.Owner, repo.Name, page)
		if err := client.DoWithContext(ctx, "GET", path, nil, &items); err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if item.PullRequest != nil {
				continue
			}
			cache.Add(item.Number, item.Title, item.State)
		}
	}
	return cache, nil
}
```

Note: update the `import` block at the top of `cache.go` to include all needed packages:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./todo/... -run TestFetchIssueCache -v
```

Expected: both tests PASS.

- [ ] **Step 5: Verify full build**

```bash
go build ./...
```

Expected: success (processor.go and runner.go still compile since we haven't changed their signatures yet).

- [ ] **Step 6: Commit**

```bash
git add todo/cache.go todo/cache_test.go
git commit -m "feat: add FetchIssueCache with pagination and PR filtering"
```

---

## Task 3: Update ProcessItem — ActionCloseByNum

**Files:**
- Modify: `todo/processor.go`
- Create: `todo/processor_test.go`

- [ ] **Step 1: Write the failing tests**

Create `todo/processor_test.go`:

```go
package todo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./todo/... -run TestProcessItem_CloseByNum -v
```

Expected: compile error — `ProcessItem` signature doesn't accept `*IssueCache` yet.

- [ ] **Step 3: Update ProcessItem signature and ActionCloseByNum case**

In `todo/processor.go`, change the `ProcessItem` signature and update the `ActionCloseByNum` case. **Leave all other cases unchanged for now.**

Old signature:
```go
func ProcessItem(ctx context.Context, client *api.RESTClient, repo repository.Repository, item Item, dryRun bool, todoFile string) Result {
```

New signature:
```go
func ProcessItem(ctx context.Context, client *api.RESTClient, repo repository.Repository, item Item, cache *IssueCache, dryRun bool, todoFile string) Result {
```

Old `ActionCloseByNum` case (lines 137–158):
```go
case ActionCloseByNum:
    state, err := getIssueState(ctx, client, repo, item.IssueNum)
    if err != nil {
        base.Err = fmt.Errorf("view #%d: %w", item.IssueNum, err)
        return base
    }
    if state != "open" {
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("#%d already closed", item.IssueNum)
        return base
    }
    if dryRun {
        base.Verb = "would-close"
        base.Detail = fmt.Sprintf("#%d: %s", item.IssueNum, item.Task)
        return base
    }
    if err := closeIssue(ctx, client, repo, item.IssueNum); err != nil {
        base.Err = fmt.Errorf("close #%d: %w", item.IssueNum, err)
        return base
    }
    base.Verb = "closed"
    base.Detail = fmt.Sprintf("#%d: %s", item.IssueNum, item.Task)
    return base
```

New `ActionCloseByNum` case:
```go
case ActionCloseByNum:
    state, found := cache.StateByNumber(item.IssueNum)
    if !found || state != "open" {
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("#%d already closed", item.IssueNum)
        return base
    }
    if dryRun {
        base.Verb = "would-close"
        base.Detail = fmt.Sprintf("#%d: %s", item.IssueNum, item.Task)
        return base
    }
    if err := closeIssue(ctx, client, repo, item.IssueNum); err != nil {
        base.Err = fmt.Errorf("close #%d: %w", item.IssueNum, err)
        return base
    }
    base.Verb = "closed"
    base.Detail = fmt.Sprintf("#%d: %s", item.IssueNum, item.Task)
    return base
```

Also update the call site in `Run` (runner.go) to pass `cache` — temporarily add a placeholder `nil` so it compiles, then fix properly in Task 6. For now, update `runner.go` line 29:

```go
results[item.LineIndex] = ProcessItem(ctx, client, repo, item, nil, dryRun, todoFile)
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./todo/... -run TestProcessItem_CloseByNum -v
```

Expected: all four tests PASS.

- [ ] **Step 5: Build check**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 6: Commit**

```bash
git add todo/processor.go todo/processor_test.go todo/runner.go
git commit -m "feat: replace getIssueState with IssueCache lookup in ActionCloseByNum"
```

---

## Task 4: Update ProcessItem — ActionCreateIssue

**Files:**
- Modify: `todo/processor.go`
- Modify: `todo/processor_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `todo/processor_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./todo/... -run TestProcessItem_CreateIssue -v
```

Expected: FAIL — `ActionCreateIssue` still calls `findOpenIssue`, not the cache.

- [ ] **Step 3: Update ActionCreateIssue case in processor.go**

Old `ActionCreateIssue` case (lines 105–134):
```go
case ActionCreateIssue:
    n, found, err := findOpenIssue(ctx, client, repo, item.Task)
    if err != nil {
        base.Err = fmt.Errorf("search %q: %w", item.Task, err)
        return base
    }
    if found {
        base.IssueNum = n
        base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
        base.Changed = base.NewLine != item.Raw
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("#%d already exists", n)
        return base
    }
    if dryRun {
        base.Verb = "would-create"
        base.Detail = item.Task
        return base
    }
    n, err = createIssue(ctx, client, repo, item.Task, todoFile)
    if err != nil {
        base.Err = fmt.Errorf("create %q: %w", item.Task, err)
        return base
    }
    base.IssueNum = n
    base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
    base.Changed = true
    base.Verb = "created"
    base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
    return base
```

New `ActionCreateIssue` case:
```go
case ActionCreateIssue:
    if n, found := cache.NumberByTitle(item.Task); found {
        base.IssueNum = n
        base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
        base.Changed = base.NewLine != item.Raw
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("#%d already exists", n)
        return base
    }
    if dryRun {
        base.Verb = "would-create"
        base.Detail = item.Task
        return base
    }
    n, err := createIssue(ctx, client, repo, item.Task, todoFile)
    if err != nil {
        base.Err = fmt.Errorf("create %q: %w", item.Task, err)
        return base
    }
    cache.Add(n, item.Task, "open")
    base.IssueNum = n
    base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
    base.Changed = true
    base.Verb = "created"
    base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
    return base
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./todo/... -run TestProcessItem_CreateIssue -v
```

Expected: all three tests PASS.

- [ ] **Step 5: Commit**

```bash
git add todo/processor.go todo/processor_test.go
git commit -m "feat: replace findOpenIssue with IssueCache lookup in ActionCreateIssue"
```

---

## Task 5: Update ProcessItem — ActionCloseByTitle, delete dead functions

**Files:**
- Modify: `todo/processor.go`
- Modify: `todo/processor_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `todo/processor_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./todo/... -run TestProcessItem_CloseByTitle -v
```

Expected: FAIL — `ActionCloseByTitle` still calls `findOpenIssue`.

- [ ] **Step 3: Update ActionCloseByTitle case and delete dead functions**

Old `ActionCloseByTitle` case (lines 160–186):
```go
case ActionCloseByTitle:
    n, found, err := findOpenIssue(ctx, client, repo, item.Task)
    if err != nil {
        base.Err = fmt.Errorf("search %q: %w", item.Task, err)
        return base
    }
    if !found {
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("no open issue found for: %s", item.Task)
        return base
    }
    if dryRun {
        base.IssueNum = n
        base.Verb = "would-close"
        base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
        return base
    }
    if err := closeIssue(ctx, client, repo, n); err != nil {
        base.Err = fmt.Errorf("close #%d: %w", n, err)
        return base
    }
    base.IssueNum = n
    base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
    base.Changed = base.NewLine != item.Raw
    base.Verb = "closed"
    base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
    return base
```

New `ActionCloseByTitle` case:
```go
case ActionCloseByTitle:
    n, found := cache.NumberByTitle(item.Task)
    if !found {
        base.Verb = "skip"
        base.Detail = fmt.Sprintf("no open issue found for: %s", item.Task)
        return base
    }
    if dryRun {
        base.IssueNum = n
        base.Verb = "would-close"
        base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
        return base
    }
    if err := closeIssue(ctx, client, repo, n); err != nil {
        base.Err = fmt.Errorf("close #%d: %w", n, err)
        return base
    }
    base.IssueNum = n
    base.NewLine = fmt.Sprintf("%s%s (#%d)", item.Prefix, item.Task, n)
    base.Changed = base.NewLine != item.Raw
    base.Verb = "closed"
    base.Detail = fmt.Sprintf("#%d: %s", n, item.Task)
    return base
```

Also delete the now-unused functions `findOpenIssue` (lines 38–52) and `getIssueState` (lines 71–78) from `processor.go`. Delete the `searchResponse` type (lines 16–21) and `issueResponse` field used only by `getIssueState` — keep `issueResponse` only if still used by `createIssue`. Check: `createIssue` uses `issueResponse` for the POST response, so keep `issueResponse`. Delete only `searchResponse` and the two functions.

Also clean up the `import` block in `processor.go` — remove `"net/url"` if no longer used (it was only used by `findOpenIssue`).

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./todo/... -run TestProcessItem_CloseByTitle -v
```

Expected: all three tests PASS.

- [ ] **Step 5: Run the full test suite**

```bash
go test ./todo/... -v
```

Expected: all tests PASS.

- [ ] **Step 6: Build check**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 7: Commit**

```bash
git add todo/processor.go todo/processor_test.go
git commit -m "feat: replace findOpenIssue with IssueCache lookup in ActionCloseByTitle; delete dead functions"
```

---

## Task 6: Update Run to accept IssueCache

**Files:**
- Modify: `todo/runner.go`

- [ ] **Step 1: Update Run signature**

In `todo/runner.go`, change the `Run` signature from:

```go
func Run(ctx context.Context, items []Item, client *api.RESTClient, repo repository.Repository, dryRun bool, todoFile string) []Result {
```

to:

```go
func Run(ctx context.Context, items []Item, client *api.RESTClient, repo repository.Repository, cache *IssueCache, dryRun bool, todoFile string) []Result {
```

And update the `ProcessItem` call inside (the `nil` placeholder from Task 3):

```go
results[item.LineIndex] = ProcessItem(ctx, client, repo, item, nil, dryRun, todoFile)
```

becomes:

```go
results[item.LineIndex] = ProcessItem(ctx, client, repo, item, cache, dryRun, todoFile)
```

- [ ] **Step 2: Build check**

```bash
go build ./...
```

Expected: compile error in `main.go` — `todo.Run` call has wrong arity. This is expected; fixed in the next task.

- [ ] **Step 3: Commit**

```bash
git add todo/runner.go
git commit -m "feat: thread IssueCache through Run and ProcessItem"
```

---

## Task 7: Update main.go — call FetchIssueCache

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Add FetchIssueCache call and update todo.Run call**

In `main.go`'s `run()` function, after the `client` is created and before `todo.Run` is called, add:

```go
cache, err := todo.FetchIssueCache(context.Background(), client, repo)
if err != nil {
    return fmt.Errorf("fetching issues: %w", err)
}
```

Then update the `todo.Run` call from:

```go
results := todo.Run(context.Background(), items, client, repo, dryRun, todoFile)
```

to:

```go
results := todo.Run(context.Background(), items, client, repo, cache, dryRun, todoFile)
```

- [ ] **Step 2: Build check**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 3: Run full test suite**

```bash
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: prefetch all issues before processing to eliminate per-item read API calls"
```

---

## Task 8: Final verification

- [ ] **Step 1: Run all tests with race detector**

```bash
go test -race ./...
```

Expected: PASS with no race conditions detected.

- [ ] **Step 2: Verify binary builds cleanly**

```bash
go build -o /tmp/gh-issue-todo-verify . && echo "build ok" && rm /tmp/gh-issue-todo-verify
```

Expected: `build ok`.

- [ ] **Step 3: Confirm dead imports are gone**

```bash
go vet ./...
```

Expected: no output (no errors).

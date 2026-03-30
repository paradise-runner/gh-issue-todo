# Bulk Issue Prefetch — Design Spec

**Date:** 2026-03-30
**Status:** Approved

## Problem

The current implementation makes one GitHub API call per TODO item to determine issue state:

- `ActionCloseByNum` → `GET /repos/.../issues/{n}` (per item)
- `ActionCreateIssue` → `GET /search/issues?q=...` (per item, tighter rate limit)
- `ActionCloseByTitle` → `GET /search/issues?q=...` (per item, tighter rate limit)

For a TODO file with N actionable items, this is O(N) read API calls. The search endpoint has a 30 req/min rate limit, making it the primary bottleneck.

## Goal

Replace all per-item read API calls with a single paginated prefetch of all repository issues, reducing read calls from O(N items) to O(N issues / 100).

## Approach: Prefetch snapshot (Option A)

Before any parallel processing, fetch all issues into an `IssueCache` struct. Pass the immutable (post-construction) cache into `Run` and `ProcessItem`. Goroutines read from the cache with no locks; the one write path (after `createIssue`) uses a `sync.RWMutex`.

## Data Structures

New `IssueCache` struct in `todo/cache.go`:

```go
type IssueCache struct {
    mu       sync.RWMutex
    byNumber map[int]string  // issue# → "open" | "closed"
    byTitle  map[string]int  // lowercase(title) → issue# (open issues only)
}
```

Methods:
- `StateByNumber(n int) (state string, found bool)` — RLock read
- `NumberByTitle(title string) (n int, found bool)` — RLock read
- `Add(n int, title, state string)` — Lock write (called after createIssue)

## Prefetch

New function `FetchIssueCache(ctx, client, repo) (*IssueCache, error)`:

- Paginates `GET /repos/{owner}/{repo}/issues?state=all&per_page=100`
- Stops when a page returns 0 items
- Filters out pull requests (items with a non-nil `pull_request` field in JSON)
- Populates `byNumber` for all issues; `byTitle` for open issues only
- Called once in `main.go`'s `run()` before `todo.Run()`; hard-fails the run on error

## Signature Changes

```go
// runner.go
func Run(ctx context.Context, items []Item, client *api.RESTClient, repo repository.Repository, cache *IssueCache, dryRun bool, todoFile string) []Result

// processor.go
func ProcessItem(ctx context.Context, client *api.RESTClient, repo repository.Repository, item Item, cache *IssueCache, dryRun bool, todoFile string) Result
```

## ProcessItem Changes

| Action | Before | After |
|---|---|---|
| `ActionCreateIssue` | `findOpenIssue(...)` HTTP call | `cache.NumberByTitle(...)` map lookup; after create, call `cache.Add(...)` |
| `ActionCloseByNum` | `getIssueState(...)` HTTP call | `cache.StateByNumber(...)` map lookup |
| `ActionCloseByTitle` | `findOpenIssue(...)` HTTP call | `cache.NumberByTitle(...)` map lookup |
| `ActionSkipLinked` | unchanged | unchanged |
| `ActionIgnore` | unchanged | unchanged |

`findOpenIssue` and `getIssueState` are deleted.

## Concurrency

- `FetchIssueCache` is called serially before goroutines start — no race on construction.
- Goroutines only read via `StateByNumber`/`NumberByTitle` (RLock) — concurrent reads are safe.
- `cache.Add` after `createIssue` uses a full Lock — write is rare and short.

## Error Handling

- Prefetch failure aborts the run with a clear error before any writes happen.
- Empty repo (no issues yet): both maps are empty; all lookup paths degrade gracefully.
- Issue number not in cache (e.g. issue created outside this run): treated as "not found" / skip.

## Testing Approach

**Red/green TDD throughout.** Write failing tests first, then implement to make them pass.

- `IssueCache` unit tests: construct directly with pre-populated maps, verify method behavior.
- `FetchIssueCache` tests: mock REST client returning canned paginated responses; verify PR filtering, pagination termination, map population.
- `ProcessItem` unit tests: construct `IssueCache` directly — no HTTP mocking needed for read paths.
- Integration: existing `Run` tests updated to pass a pre-built cache.

## Files Affected

- `todo/cache.go` — new file: `IssueCache` struct + `FetchIssueCache`
- `todo/processor.go` — remove `findOpenIssue`, `getIssueState`; update `ProcessItem` signature and action cases
- `todo/runner.go` — update `Run` signature
- `main.go` — call `FetchIssueCache` before `todo.Run`
- `todo/cache_test.go` — new file
- `todo/processor_test.go` — updated

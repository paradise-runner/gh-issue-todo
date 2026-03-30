package todo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
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

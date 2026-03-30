package todo

import (
	"context"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"golang.org/x/sync/errgroup"
)

// MaxConcurrency is the default cap on simultaneous GitHub API calls.
// Conservative enough to stay well under GitHub's secondary rate limits.
const MaxConcurrency = 5

// Run fans out all items in parallel (bounded by MaxConcurrency) and returns
// results in the same order as items. Errors from individual items are stored
// in Result.Err rather than aborting the group — a 404 on one item should not
// prevent the other 14 from being processed.
func Run(ctx context.Context, items []Item, client *api.RESTClient, repo repository.Repository, dryRun bool, todoFile string) []Result {
	results := make([]Result, len(items))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(MaxConcurrency)

	for _, item := range items {
		item := item // capture loop variable
		g.Go(func() error {
			// Each goroutine writes to its own disjoint index — no mutex needed.
			results[item.LineIndex] = ProcessItem(ctx, client, repo, item, nil, dryRun, todoFile)
			return nil // never propagate errors to the group
		})
	}
	g.Wait() //nolint:errcheck // goroutines always return nil
	return results
}

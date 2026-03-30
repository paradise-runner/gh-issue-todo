package todo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// searchResponse is the shape returned by GET /search/issues.
type searchResponse struct {
	Items []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"items"`
}

// issueResponse is used for GET /repos/{owner}/{repo}/issues/{n}.
type issueResponse struct {
	Number int    `json:"number"`
	State  string `json:"state"` // "open" or "closed"
}

func jsonBody(v any) (*bytes.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

// findOpenIssue searches for an open issue with exactly this title.
// Returns (issueNumber, found, error).
func findOpenIssue(ctx context.Context, client *api.RESTClient, repo repository.Repository, title string) (int, bool, error) {
	q := url.QueryEscape(`"`+title+`"`) + fmt.Sprintf("+in:title+repo:%s/%s+is:open+is:issue", repo.Owner, repo.Name)
	var resp searchResponse
	if err := client.DoWithContext(ctx, "GET", "search/issues?q="+q, nil, &resp); err != nil {
		return 0, false, err
	}
	// Filter client-side for exact match — GitHub search is fuzzy even with quotes.
	for _, item := range resp.Items {
		if strings.EqualFold(item.Title, title) {
			return item.Number, true, nil
		}
	}
	return 0, false, nil
}

// createIssue creates a new issue and returns its number.
func createIssue(ctx context.Context, client *api.RESTClient, repo repository.Repository, title, todoFile string) (int, error) {
	body, err := jsonBody(map[string]string{
		"title": title,
		"body":  fmt.Sprintf("Synced from `%s`.", todoFile),
	})
	if err != nil {
		return 0, err
	}
	var resp issueResponse
	path := fmt.Sprintf("repos/%s/%s/issues", repo.Owner, repo.Name)
	if err := client.DoWithContext(ctx, "POST", path, body, &resp); err != nil {
		return 0, err
	}
	return resp.Number, nil
}

// getIssueState returns "open" or "closed" for issue number n.
func getIssueState(ctx context.Context, client *api.RESTClient, repo repository.Repository, n int) (string, error) {
	var resp issueResponse
	path := fmt.Sprintf("repos/%s/%s/issues/%d", repo.Owner, repo.Name, n)
	if err := client.DoWithContext(ctx, "GET", path, nil, &resp); err != nil {
		return "", err
	}
	return resp.State, nil
}

// closeIssue closes issue number n.
func closeIssue(ctx context.Context, client *api.RESTClient, repo repository.Repository, n int) error {
	body, err := jsonBody(map[string]string{"state": "closed"})
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/%s/issues/%d", repo.Owner, repo.Name, n)
	return client.DoWithContext(ctx, "PATCH", path, body, nil)
}

// ProcessItem runs the full lifecycle for one Item and returns a Result.
// Errors are stored in Result.Err rather than returned directly.
func ProcessItem(ctx context.Context, client *api.RESTClient, repo repository.Repository, item Item, cache *IssueCache, dryRun bool, todoFile string) Result {
	base := Result{LineIndex: item.LineIndex, NewLine: item.Raw, Task: item.Task, IssueNum: item.IssueNum}

	switch item.Action {
	case ActionIgnore:
		return base

	case ActionSkipLinked:
		base.Verb = "skip"
		base.Detail = fmt.Sprintf("#%d already linked", item.IssueNum)
		return base

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
	}

	return base
}

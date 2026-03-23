package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/edwardchampion/gh-issue-todo/todo"
)

func main() {
	var todoFile string
	var dryRun bool

	flag.StringVar(&todoFile, "file", "TODO.md", "Path to TODO file")
	flag.StringVar(&todoFile, "f", "TODO.md", "Path to TODO file (shorthand)")
	flag.BoolVar(&dryRun, "dry-run", false, "Print actions without making changes")
	flag.BoolVar(&dryRun, "n", false, "Print actions without making changes (shorthand)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `Sync TODO.md checkboxes with GitHub issues.

USAGE
  gh issue-todo [flags]

FLAGS
  -f, --file <path>   Path to TODO file (default: TODO.md)
  -n, --dry-run       Print actions without making changes
  -h, --help          Show this help

BEHAVIOR
  - [ ] Task          Creates a GitHub issue; rewrites line to "- [ ] Task (#N)"
  - [ ] Task (#N)     Already linked — skipped
  - [x] Task (#N)     Closes issue #N
  - [x] Task          Searches for an open issue by title; closes if found`)
	}
	flag.Parse()

	if err := run(todoFile, dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(todoFile string, dryRun bool) error {
	if _, err := os.Stat(todoFile); os.IsNotExist(err) {
		return fmt.Errorf("'%s' not found", todoFile)
	}

	repo, err := repository.Current()
	if err != nil {
		return fmt.Errorf("not inside a GitHub repository (or not authenticated): %w", err)
	}

	client, err := api.DefaultRESTClient()
	if err != nil {
		return fmt.Errorf("creating API client: %w", err)
	}

	lines, trailingNewline, err := todo.ReadFile(todoFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", todoFile, err)
	}

	fmt.Fprintf(os.Stderr, "syncing %s → %s/%s\n", todoFile, repo.Owner, repo.Name)

	items := todo.ParseLines(lines)
	results := todo.Run(context.Background(), items, client, repo, dryRun, todoFile)

	// Print results in original line order and apply updates to the lines slice.
	changed := false
	var errs []string
	for _, r := range results {
		if r.Verb == "" {
			continue
		}
		fmt.Printf("%-12s %s\n", r.Verb, r.Detail)
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
			continue
		}
		if r.Changed {
			lines[r.LineIndex] = r.NewLine
			changed = true
		}
	}

	if changed && !dryRun {
		if err := writeBack(todoFile, lines, trailingNewline); err != nil {
			return fmt.Errorf("writing %s: %w", todoFile, err)
		}
		fmt.Printf("updated %s\n", todoFile)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d item(s) failed:\n  %s", len(errs), strings.Join(errs, "\n  "))
	}
	return nil
}

// writeBack writes lines to path atomically via a temp file + rename.
func writeBack(path string, lines []string, trailingNewline bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".todo-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if rename succeeded
	}()

	w := bufio.NewWriter(tmp)
	for i, line := range lines {
		if i > 0 {
			w.WriteByte('\n')
		}
		w.WriteString(line)
	}
	if trailingNewline {
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

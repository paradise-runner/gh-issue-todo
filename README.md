# gh-issue-todo

A [GitHub CLI](https://cli.github.com/) extension that syncs a `TODO.md` file with GitHub issues.

## Install

```sh
gh extension install paradise-runner/gh-issue-todo
```

Or from a local clone:

```sh
gh extension install .
```

## Usage

```sh
gh issue-todo [flags]
```

| Flag | Description |
|------|-------------|
| `-f, --file <path>` | Path to TODO file (default: `TODO.md`) |
| `-n, --dry-run` | Print planned actions without making changes |
| `-h, --help` | Show help |

## How it works

The extension reads your `TODO.md` and acts on every checkbox line:

| Line | Action |
|------|--------|
| `- [ ] Task` | Creates a GitHub issue titled `Task`; rewrites line to `- [ ] Task (#N)` |
| `- [ ] Task (#N)` | Issue already linked — skipped |
| `- [x] Task (#N)` | Closes issue `#N` |
| `- [x] Task` | Searches for an open issue titled `Task`; closes it if found |

Issue numbers are written back into `TODO.md` so future runs skip redundant API calls and closures are unambiguous.

## Example

**TODO.md before:**

```markdown
## Sprint

- [ ] Set up CI pipeline
- [ ] Write integration tests
- [x] Define API schema
```

**Run:**

```sh
gh issue-todo
# created #12: Set up CI pipeline
# created #13: Write integration tests
# skip  no open issue found for: Define API schema
# updated TODO.md
```

**TODO.md after:**

```markdown
## Sprint

- [ ] Set up CI pipeline (#12)
- [ ] Write integration tests (#13)
- [x] Define API schema
```

Later, after finishing a task:

```markdown
- [x] Set up CI pipeline (#12)
```

```sh
gh issue-todo
# closed #12: Set up CI pipeline
```

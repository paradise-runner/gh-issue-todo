package todo

// Action describes what should be done with a parsed TODO item.
type Action int

const (
	ActionSkipLinked   Action = iota // "- [ ] Task (#N)" — open, already linked; skip
	ActionCreateIssue                // "- [ ] Task"       — open, no link; create issue
	ActionCloseByNum                 // "- [x] Task (#N)" — closed, linked; close by number
	ActionCloseByTitle               // "- [x] Task"       — closed, no link; find and close
	ActionIgnore                     // non-checkbox line; pass through unchanged
)

// Item is an immutable snapshot of one parsed TODO.md line.
type Item struct {
	LineIndex int    // index into the original lines slice, used for ordered result collection
	Raw       string // original line text
	Prefix    string // everything up to and including the space after "]", e.g. "- [ ] "
	Task      string // task title, stripped of any trailing " (#N)"
	IssueNum  int    // issue number from "(#N)", or 0 if not present
	Action    Action
}

// Result is what a goroutine returns after processing one Item.
type Result struct {
	LineIndex int    // same as Item.LineIndex
	NewLine   string // replacement line content (may equal Item.Raw if unchanged)
	Changed   bool   // true if NewLine differs from Item.Raw
	Verb      string // one-word action for output: "created", "closed", "skip", "would-create", "would-close"
	Detail    string // human-readable suffix for the output line
	IssueNum  int    // issue number involved (0 if none)
	Task      string // task title
	Err       error
}

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

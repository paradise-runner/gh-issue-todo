package todo

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
)

// Regexes are compiled once. Linked variants must be tried before unlinked ones
// so that "(#N)" is not absorbed into the task title.
var (
	reOpenLinked = regexp.MustCompile(
		`^(?P<prefix>\s*(?:-|[0-9]+\.)\s+\[\s\]\s+)(?P<task>.+?)\s+\(#(?P<num>[0-9]+)\)\s*$`,
	)
	reOpenUnlinked = regexp.MustCompile(
		`^(?P<prefix>\s*(?:-|[0-9]+\.)\s+\[\s\]\s+)(?P<task>.+)$`,
	)
	reClosedLinked = regexp.MustCompile(
		`^(?P<prefix>\s*(?:-|[0-9]+\.)\s+\[[xX]\]\s+)(?P<task>.+?)\s+\(#(?P<num>[0-9]+)\)\s*$`,
	)
	reClosedUnlinked = regexp.MustCompile(
		`^(?P<prefix>\s*(?:-|[0-9]+\.)\s+\[[xX]\]\s+)(?P<task>.+)$`,
	)
)

func namedGroup(re *regexp.Regexp, match []string, name string) string {
	i := re.SubexpIndex(name)
	if i < 0 || i >= len(match) {
		return ""
	}
	return match[i]
}

// ParseLine classifies a single raw line into an Item.
func ParseLine(index int, raw string) Item {
	base := Item{LineIndex: index, Raw: raw, Action: ActionIgnore}

	if m := reOpenLinked.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(namedGroup(reOpenLinked, m, "num"))
		return Item{
			LineIndex: index, Raw: raw, Action: ActionSkipLinked,
			Prefix:   namedGroup(reOpenLinked, m, "prefix"),
			Task:     namedGroup(reOpenLinked, m, "task"),
			IssueNum: n,
		}
	}
	if m := reOpenUnlinked.FindStringSubmatch(raw); m != nil {
		return Item{
			LineIndex: index, Raw: raw, Action: ActionCreateIssue,
			Prefix: namedGroup(reOpenUnlinked, m, "prefix"),
			Task:   namedGroup(reOpenUnlinked, m, "task"),
		}
	}
	if m := reClosedLinked.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(namedGroup(reClosedLinked, m, "num"))
		return Item{
			LineIndex: index, Raw: raw, Action: ActionCloseByNum,
			Prefix:   namedGroup(reClosedLinked, m, "prefix"),
			Task:     namedGroup(reClosedLinked, m, "task"),
			IssueNum: n,
		}
	}
	if m := reClosedUnlinked.FindStringSubmatch(raw); m != nil {
		return Item{
			LineIndex: index, Raw: raw, Action: ActionCloseByTitle,
			Prefix: namedGroup(reClosedUnlinked, m, "prefix"),
			Task:   namedGroup(reClosedUnlinked, m, "task"),
		}
	}

	return base
}

// ReadFile reads all lines from path and returns them, plus whether the file
// had a trailing newline (so write-back can preserve it).
func ReadFile(path string) (lines []string, trailingNewline bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}

	// Detect trailing newline by checking the last byte of the file.
	info, err := f.Stat()
	if err != nil {
		return lines, true, nil // assume trailing newline on stat failure
	}
	if info.Size() == 0 {
		return lines, false, nil
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, info.Size()-1); err == nil {
		trailingNewline = buf[0] == '\n'
	}
	return lines, trailingNewline, nil
}

// ParseLines turns a slice of raw lines into Items, one per line.
func ParseLines(lines []string) []Item {
	items := make([]Item, len(lines))
	for i, line := range lines {
		items[i] = ParseLine(i, line)
	}
	return items
}

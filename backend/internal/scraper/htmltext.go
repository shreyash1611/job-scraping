package scraper

import (
	"html"
	"regexp"
	"strings"
)

var (
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
	// \x{00a0} is a non-breaking space, which HTML job descriptions are full
	// of and which \s in Go's regexp does not match.
	horizSpaceRe = regexp.MustCompile(`[ \t\x{00a0}]+`)
)

// stripHTML flattens an HTML job description into plain text. Every tag
// becomes a line break, which keeps qualification bullets on their own lines
// - that matters because n8n's "Filter Jobs" node runs line-oriented regexes
// over the description to pull out YoE requirements.
//
// Shared by every scraper that gets HTML back, which is most of them.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}

	// Greenhouse serves its description HTML entity-escaped (a "<p>" arrives
	// as "&lt;p&gt;") while Google and Workday serve real tags, so unescaping
	// up front normalizes both into the same shape before anything is
	// stripped. The second unescape below then handles entities that were
	// only ever meant as text, e.g. a literal "&nbsp;" that arrived as
	// "&amp;nbsp;".
	s = html.UnescapeString(s)
	s = htmlTagRe.ReplaceAllString(s, "\n")
	s = html.UnescapeString(s)
	s = horizSpaceRe.ReplaceAllString(s, " ")

	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

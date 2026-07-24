package scraper

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// GoogleScraper fetches Google's public careers search results page and
// extracts the job listings embedded in it as an AF_initDataCallback JS
// blob - Google Careers has no REST API, but does server-side render this
// data block into the HTML, so a plain GET + parse is enough (no headless
// browser needed).
type GoogleScraper struct{}

func NewGoogleScraper() *GoogleScraper {
	return &GoogleScraper{}
}

var googleDataBlockRe = regexp.MustCompile(`(?s)AF_initDataCallback\((\{.*?\})\);`)

func (g *GoogleScraper) Search(params SearchParams) ([]JobPosting, error) {
	var all []JobPosting

	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := g.searchOneRole(role, params.Locations)
		if err != nil {
			return nil, fmt.Errorf("google: role %q: %w", role, err)
		}
		all = append(all, jobs...)
	}

	all = DedupeByURL(all)
	all = FilterOutInternships(all)
	all = FilterByLocation(all, params.Locations)
	return all, nil
}

func (g *GoogleScraper) searchOneRole(role string, locations []string) ([]JobPosting, error) {
	endpoint := "https://www.google.com/about/careers/applications/jobs/results"

	q := url.Values{}
	q.Set("q", role)
	if hint := googleLocationHint(locations); hint != "" {
		// Google's location param only accepts one free-text, geocodable
		// place at a time; pass a single hint and let FilterByLocation do
		// the real narrowing across all requested locations afterwards.
		q.Set("location", hint)
	}

	req, err := newRequest(http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	rawJobs, err := extractGoogleJobs(string(body))
	if err != nil {
		return nil, err
	}

	jobs := make([]JobPosting, 0, len(rawJobs))
	for _, raw := range rawJobs {
		if job := parseGoogleJob(raw); job != nil {
			jobs = append(jobs, *job)
		}
	}
	return jobs, nil
}

// extractGoogleJobs finds every AF_initDataCallback(...) blob in the page,
// parses each one's "data" payload as JSON, and returns the first one whose
// first element looks like a list of job entries. Google's internal key
// names (e.g. "ds:1") aren't guaranteed stable, so we detect the right block
// by shape rather than by key name.
//
// That results block is always length 4 - [jobListOrNull, null, offset,
// pageSize] - where index 0 is JSON null specifically when the query
// matched zero jobs, not just any time it's not a job-entry array. A query
// like an Amazon-only title format ("Software Dev Engineer I FTC") sent to
// Google will legitimately hit this null case, and that's a normal "no
// results," not a parser/structure failure - so it's tracked separately and
// only used as a fallback, in case a *later* block in the page turns out to
// hold real results after all.
func extractGoogleJobs(pageHTML string) ([][]interface{}, error) {
	matches := googleDataBlockRe.FindAllStringSubmatch(pageHTML, -1)

	sawEmptyResultsBlock := false

	for _, m := range matches {
		dataArrJSON, ok := extractDataArray(m[1])
		if !ok {
			continue
		}

		var parsed []interface{}
		if err := json.Unmarshal([]byte(dataArrJSON), &parsed); err != nil {
			continue
		}
		if len(parsed) == 0 {
			continue
		}

		if len(parsed) == 4 && parsed[0] == nil {
			sawEmptyResultsBlock = true
			continue
		}

		jobList, ok := parsed[0].([]interface{})
		if !ok || len(jobList) == 0 || !looksLikeJobEntry(jobList[0]) {
			continue
		}

		jobs := make([][]interface{}, 0, len(jobList))
		for _, j := range jobList {
			if entry, ok := j.([]interface{}); ok {
				jobs = append(jobs, entry)
			}
		}
		return jobs, nil
	}

	if sawEmptyResultsBlock {
		return nil, nil
	}

	return nil, fmt.Errorf("could not find job data block in Google careers page (site structure may have changed)")
}

// googleLocationHint picks a geocodable place name to send as Google's
// "location" query param, skipping non-geographic terms like "Remote"
// which Google's location field can't resolve and would zero out results
// entirely rather than just not filtering.
func googleLocationHint(locations []string) string {
	for _, loc := range locations {
		loc = strings.TrimSpace(loc)
		if loc == "" || strings.EqualFold(loc, "remote") {
			continue
		}
		return loc
	}
	return ""
}

func looksLikeJobEntry(v interface{}) bool {
	entry, ok := v.([]interface{})
	if !ok || len(entry) < 10 {
		return false
	}
	_, idIsString := entry[0].(string)
	_, titleIsString := entry[1].(string)
	return idIsString && titleIsString
}

// extractDataArray pulls the JSON array following "data:" out of a blob
// shaped like {key: 'ds:1', hash: '2', data:[...], sideChannel: {}}.
func extractDataArray(blob string) (string, bool) {
	idx := strings.Index(blob, "data:")
	if idx == -1 {
		return "", false
	}
	rest := blob[idx+len("data:"):]

	end := strings.LastIndex(rest, ", sideChannel:")
	if end == -1 {
		return "", false
	}
	return rest[:end], true
}

// parseGoogleJob maps a positional job entry (see extractGoogleJobs) into
// our JobPosting contract. Field positions were determined by inspecting a
// live response on 2026-07-22; Google could change this layout without
// notice since it's undocumented.
func parseGoogleJob(entry []interface{}) *JobPosting {
	id, _ := entry[0].(string)
	title, _ := entry[1].(string)
	applyURL, _ := entry[2].(string)
	if id == "" || title == "" {
		return nil
	}

	company := "Google"
	if c, ok := safeIndex(entry, 7).(string); ok && c != "" {
		company = c
	}

	location := extractGoogleLocations(safeIndex(entry, 9))

	description := strings.TrimSpace(strings.Join([]string{
		extractGoogleRichText(safeIndex(entry, 10)), // overview
		extractGoogleRichText(safeIndex(entry, 3)),  // responsibilities
		extractGoogleRichText(safeIndex(entry, 4)),  // qualifications
	}, "\n\n"))

	postedDate := extractGoogleTimestamp(safeIndex(entry, 12))

	if applyURL == "" {
		applyURL = "https://www.google.com/about/careers/applications/jobs/results?q=" + url.QueryEscape(title)
	}

	return &JobPosting{
		Company:     company,
		Title:       title,
		URL:         applyURL,
		Location:    location,
		Description: description,
		PostedDate:  postedDate,
	}
}

func safeIndex(entry []interface{}, i int) interface{} {
	if i < len(entry) {
		return entry[i]
	}
	return nil
}

// extractGoogleLocations turns a value shaped like
// [["Bengaluru, Karnataka, India", [...streetAddr], "Bengaluru", ...], ...]
// into a single "; "-separated string of formatted addresses.
func extractGoogleLocations(v interface{}) string {
	locs, ok := v.([]interface{})
	if !ok {
		return ""
	}

	parts := make([]string, 0, len(locs))
	for _, l := range locs {
		locEntry, ok := l.([]interface{})
		if !ok || len(locEntry) == 0 {
			continue
		}
		if formatted, ok := locEntry[0].(string); ok && formatted != "" {
			parts = append(parts, formatted)
		}
	}
	return strings.Join(parts, "; ")
}

// extractGoogleRichText handles fields shaped like [null, "<p>html</p>"]
// and strips them down to plain text.
func extractGoogleRichText(v interface{}) string {
	pair, ok := v.([]interface{})
	if !ok || len(pair) < 2 {
		return ""
	}
	raw, ok := pair[1].(string)
	if !ok {
		return ""
	}
	return stripHTML(raw)
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "\n")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

// extractGoogleTimestamp handles fields shaped like [unixSeconds, nanos]
// and formats them as YYYY-MM-DD.
func extractGoogleTimestamp(v interface{}) string {
	pair, ok := v.([]interface{})
	if !ok || len(pair) == 0 {
		return ""
	}
	seconds, ok := pair[0].(float64)
	if !ok {
		return ""
	}
	return time.Unix(int64(seconds), 0).UTC().Format("2006-01-02")
}

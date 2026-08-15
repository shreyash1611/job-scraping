package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GreenhouseScraper reads a company's public Greenhouse board through
// Greenhouse's own board API. This is the cheapest source in the package by
// far: one request with ?content=true returns every posting on the board with
// its full description inline, so unlike Workday there are no per-job detail
// fetches and no pagination to walk.
type GreenhouseScraper struct {
	Company   string
	BoardSlug string
}

func NewDatabricksScraper() *GreenhouseScraper {
	return &GreenhouseScraper{Company: "Databricks", BoardSlug: "databricks"}
}

func NewOktaScraper() *GreenhouseScraper {
	return &GreenhouseScraper{Company: "Okta", BoardSlug: "okta"}
}

type greenhouseBoardResponse struct {
	Jobs []greenhouseJob `json:"jobs"`
	Meta struct {
		Total int `json:"total"`
	} `json:"meta"`
}

type greenhouseJob struct {
	Title       string `json:"title"`
	AbsoluteURL string `json:"absolute_url"`
	// Content is the full description as entity-escaped HTML - stripHTML
	// handles that shape, see the comment there.
	Content  string `json:"content"`
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
	FirstPublished string `json:"first_published"`
	UpdatedAt      string `json:"updated_at"`
}

func (g *GreenhouseScraper) Search(params SearchParams) ([]JobPosting, error) {
	board, err := g.fetchBoard()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.ToLower(g.Company), err)
	}

	// A board that parses but is completely empty means the slug stopped
	// resolving to a real board, which is worth an error rather than a
	// silent zero-result day.
	if len(board.Jobs) == 0 {
		return nil, fmt.Errorf("%s: greenhouse board %q returned no postings at all (board slug may have changed)",
			strings.ToLower(g.Company), g.BoardSlug)
	}

	jobs := make([]JobPosting, 0, len(board.Jobs))
	for _, j := range board.Jobs {
		if !titleMatchesAnyRole(j.Title, params.Roles) {
			continue
		}
		jobs = append(jobs, JobPosting{
			Company:     g.Company,
			Title:       strings.TrimSpace(j.Title),
			URL:         strings.TrimSpace(j.AbsoluteURL),
			Location:    strings.TrimSpace(j.Location.Name),
			Description: stripHTML(j.Content),
			PostedDate:  greenhousePostedDate(j),
		})
	}

	jobs = DedupeByURL(jobs)
	jobs = FilterOutInternships(jobs)
	jobs = FilterByLocation(jobs, params.Locations)
	return jobs, nil
}

func (g *GreenhouseScraper) fetchBoard() (*greenhouseBoardResponse, error) {
	// content=true inlines every description, which makes this response big
	// (~8.5 MB for Databricks' 800 postings) but still far quicker than the
	// hundreds of round trips the alternative would cost.
	endpoint := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs?content=true", g.BoardSlug)

	req, err := newRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("greenhouse board %q does not exist", g.BoardSlug)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed greenhouseBoardResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode board response: %w", err)
	}
	return &parsed, nil
}

// titleMatchesAnyRole reports whether a job title matches any requested role.
//
// Greenhouse's board API has no server-side search, so role matching has to
// happen here: a role matches when every word in it appears somewhere in the
// title. That's deliberately generous - it lets "Backend Engineer" match
// "Engineering Manager - Backend" - both because it mirrors how loosely the
// Workday and Amazon search endpoints treat a query, and because n8n's
// "Filter Jobs" node is what actually rejects senior and off-target titles.
func titleMatchesAnyRole(title string, roles []string) bool {
	if len(roles) == 0 {
		return true
	}

	haystack := strings.ToLower(title)
	for _, role := range roles {
		matched := false
		for _, word := range strings.Fields(strings.ToLower(role)) {
			// Single characters are skipped because they constrain nothing -
			// the "I" in "Software Development Engineer I" would otherwise
			// match on any title containing the letter.
			if len(word) < 2 {
				continue
			}
			if !strings.Contains(haystack, word) {
				matched = false
				break
			}
			matched = true
		}
		if matched {
			return true
		}
	}
	return false
}

// greenhousePostedDate prefers first_published, the date the posting went
// live, and falls back to updated_at for the rare posting that has no
// first_published set.
func greenhousePostedDate(job greenhouseJob) string {
	for _, candidate := range []string{job.FirstPublished, job.UpdatedAt} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, candidate)
		if err != nil {
			return candidate
		}
		return parsed.UTC().Format(time.RFC3339)
	}
	return ""
}

package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AshbyScraper reads a company's public Ashby job board.
//
// Like Greenhouse this is a single unauthenticated request that returns the
// whole board with descriptions inline, so there is no pagination to walk and
// no per-job detail fetch. Ashby is worth supporting separately because a
// cluster of companies sits on it exclusively - Snowflake, Confluent, OpenAI
// and Notion among them - and none of them are reachable any other way.
type AshbyScraper struct {
	Company   string
	BoardSlug string
}

type ashbyBoardResponse struct {
	Jobs []ashbyJob `json:"jobs"`
}

type ashbyJob struct {
	Title    string `json:"title"`
	Location string `json:"location"`
	JobURL   string `json:"jobUrl"`
	// Ashby serves both a plain-text and an HTML rendering of the
	// description. Preferring the plain one skips a strip step and avoids
	// any chance of mangling the qualification bullets that n8n's YoE
	// regexes read line by line.
	DescriptionPlain string `json:"descriptionPlain"`
	DescriptionHTML  string `json:"descriptionHtml"`
	PublishedAt      string `json:"publishedAt"`
	IsListed         bool   `json:"isListed"`
	// SecondaryLocations carries the extra offices on a multi-site posting,
	// which is how an India-eligible role can arrive with a US primary
	// location.
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
}

func (a *AshbyScraper) Search(params SearchParams) ([]JobPosting, error) {
	board, err := a.fetchBoard()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.ToLower(a.Company), err)
	}

	// An empty board means the slug stopped resolving rather than that the
	// company paused hiring, so it is an error and not a quiet zero.
	if len(board.Jobs) == 0 {
		return nil, fmt.Errorf("%s: ashby board %q returned no postings at all (board slug may have changed)",
			strings.ToLower(a.Company), a.BoardSlug)
	}

	jobs := make([]JobPosting, 0, len(board.Jobs))
	for _, j := range board.Jobs {
		if !j.IsListed {
			continue
		}
		if !titleMatchesAnyRole(j.Title, params.Roles) {
			continue
		}

		description := j.DescriptionPlain
		if strings.TrimSpace(description) == "" {
			description = stripHTML(j.DescriptionHTML)
		}

		jobs = append(jobs, JobPosting{
			Company:     a.Company,
			Title:       strings.TrimSpace(j.Title),
			URL:         strings.TrimSpace(j.JobURL),
			Location:    ashbyLocations(j),
			Description: description,
			PostedDate:  ashbyPostedDate(j.PublishedAt),
		})
	}

	jobs = DedupeByURL(jobs)
	jobs = FilterOutInternships(jobs)
	jobs = FilterByLocation(jobs, params.Locations)
	return jobs, nil
}

func (a *AshbyScraper) fetchBoard() (*ashbyBoardResponse, error) {
	endpoint := fmt.Sprintf("https://api.ashbyhq.com/posting-api/job-board/%s", a.BoardSlug)

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
		return nil, fmt.Errorf("ashby board %q does not exist", a.BoardSlug)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed ashbyBoardResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode board response: %w", err)
	}
	return &parsed, nil
}

// ashbyLocations joins the primary and secondary offices, so FilterByLocation
// sees every place a multi-site posting is open in.
func ashbyLocations(job ashbyJob) string {
	parts := make([]string, 0, 1+len(job.SecondaryLocations))
	if primary := strings.TrimSpace(job.Location); primary != "" {
		parts = append(parts, primary)
	}
	for _, secondary := range job.SecondaryLocations {
		if extra := strings.TrimSpace(secondary.Location); extra != "" {
			parts = append(parts, extra)
		}
	}
	return strings.Join(parts, "; ")
}

func ashbyPostedDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return parsed.UTC().Format(time.RFC3339)
}

func ashbyRegistrations() []Registration {
	boards := []struct{ slug, company, board string }{
		{"snowflake", "Snowflake", "snowflake"},
		{"confluent", "Confluent", "confluent"},
		{"openai", "OpenAI", "openai"},
		{"notion", "Notion", "notion"},
		{"temporal", "Temporal", "temporal"},
		{"plaid", "Plaid", "plaid"},
		{"supabase", "Supabase", "supabase"},
		{"miro", "Miro", "miro"},
		{"perplexity", "Perplexity", "perplexity"},
		{"sierra", "Sierra", "sierra"},
	}

	regs := make([]Registration, 0, len(boards))
	for _, b := range boards {
		b := b
		regs = append(regs, Registration{Slug: b.slug, Group: GroupProduct, New: func() Scraper {
			return &AshbyScraper{Company: b.company, BoardSlug: b.board}
		}})
	}
	return regs
}

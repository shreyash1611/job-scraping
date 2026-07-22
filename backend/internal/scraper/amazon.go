package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AmazonScraper hits Amazon's own search.json endpoint, the same one
// amazon.jobs's frontend uses to populate search results client-side.
type AmazonScraper struct{}

func NewAmazonScraper() *AmazonScraper {
	return &AmazonScraper{}
}

type amazonSearchResponse struct {
	Hits int         `json:"hits"`
	Jobs []amazonJob `json:"jobs"`
}

type amazonJob struct {
	Title                   string `json:"title"`
	JobPath                 string `json:"job_path"`
	Location                string `json:"location"`
	PostedDate              string `json:"posted_date"`
	Description             string `json:"description"`
	BasicQualifications     string `json:"basic_qualifications"`
	PreferredQualifications string `json:"preferred_qualifications"`
	Responsibilities        string `json:"responsibilities"`
}

func (a *AmazonScraper) Search(params SearchParams) ([]JobPosting, error) {
	var all []JobPosting
	countryCode := amazonCountryCodeFor(params.Locations)

	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := a.searchOneRole(role, countryCode)
		if err != nil {
			return nil, fmt.Errorf("amazon: role %q: %w", role, err)
		}
		all = append(all, jobs...)
	}

	all = DedupeByURL(all)
	// Still apply our own filter as a safety net even when countryCode
	// narrowed the query server-side - e.g. to further narrow to specific
	// cities, or catch "Remote" postings tagged in ways country= doesn't
	// distinguish.
	all = FilterByLocation(all, params.Locations)
	return all, nil
}

// amazonCountryCodeFor does a best-effort mapping from the free-text
// locations list to Amazon's `country` search param. Amazon's search only
// supports narrowing by country/city facets, not arbitrary free text, so we
// only handle the case this project actually needs (India) and otherwise
// leave the query unfiltered server-side, relying on FilterByLocation
// afterwards.
func amazonCountryCodeFor(locations []string) string {
	for _, loc := range locations {
		if strings.EqualFold(strings.TrimSpace(loc), "india") {
			return "IND"
		}
	}
	return ""
}

func (a *AmazonScraper) searchOneRole(role, countryCode string) ([]JobPosting, error) {
	endpoint := "https://www.amazon.jobs/en/search.json"

	q := url.Values{}
	q.Set("base_query", role)
	q.Set("result_limit", "50")
	q.Set("offset", "0")
	q.Set("sort", "recent")
	if countryCode != "" {
		q.Set("country", countryCode)
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

	var parsed amazonSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	jobs := make([]JobPosting, 0, len(parsed.Jobs))
	for _, j := range parsed.Jobs {
		jobURL := j.JobPath
		if jobURL != "" && !strings.HasPrefix(jobURL, "http") {
			jobURL = "https://www.amazon.jobs" + jobURL
		}

		jobs = append(jobs, JobPosting{
			Company:     "Amazon",
			Title:       j.Title,
			URL:         jobURL,
			Location:    j.Location,
			Description: buildAmazonDescription(j),
			PostedDate:  j.PostedDate,
		})
	}
	return jobs, nil
}

// buildAmazonDescription concatenates whichever description-ish fields
// Amazon's search response actually populated - the search endpoint's
// fields vary in how much detail they include, unlike the full job detail
// page.
func buildAmazonDescription(j amazonJob) string {
	parts := make([]string, 0, 4)
	for _, part := range []string{j.Description, j.BasicQualifications, j.PreferredQualifications, j.Responsibilities} {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, strings.TrimSpace(part))
		}
	}
	return strings.Join(parts, "\n\n")
}

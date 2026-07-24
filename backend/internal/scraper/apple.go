package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// AppleScraper hits jobs.apple.com's search page directly. Apple has no
// public REST API, but the search page is server-rendered (React Router
// framework mode) with the full result set embedded as a JSON blob assigned
// to `window.__staticRouterHydrationData` - a plain GET + parse is enough,
// same idea as the Google scraper.
type AppleScraper struct{}

func NewAppleScraper() *AppleScraper {
	return &AppleScraper{}
}

// appleHydrationRe matches the assignment of window.__staticRouterHydrationData.
// The value is a JS string literal passed to JSON.parse(...), i.e. the real
// payload is escaped twice - once for the JS string, once for the JSON
// inside it - see extractAppleSearchResults.
var appleHydrationRe = regexp.MustCompile(`(?s)window\.__staticRouterHydrationData\s*=\s*JSON\.parse\("(.*)"\);\s*</script>`)

type appleHydrationPayload struct {
	LoaderData struct {
		Search struct {
			SearchResults []appleSearchResult `json:"searchResults"`
		} `json:"search"`
	} `json:"loaderData"`
}

type appleSearchResult struct {
	ID                      string          `json:"id"`
	JobSummary              string          `json:"jobSummary"`
	PostingTitle            string          `json:"postingTitle"`
	TransformedPostingTitle string          `json:"transformedPostingTitle"`
	PostDateInGMT           string          `json:"postDateInGMT"`
	Locations               []appleLocation `json:"locations"`
	Team                    appleTeam       `json:"team"`
}

type appleLocation struct {
	Name          string `json:"name"`
	StateProvince string `json:"stateProvince"`
	CountryName   string `json:"countryName"`
}

type appleTeam struct {
	TeamCode string `json:"teamCode"`
}

func (a *AppleScraper) Search(params SearchParams) ([]JobPosting, error) {
	var all []JobPosting
	locationFacet := appleLocationFacetFor(params.Locations)

	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := a.searchOneRole(role, locationFacet)
		if err != nil {
			return nil, fmt.Errorf("apple: role %q: %w", role, err)
		}
		all = append(all, jobs...)
	}

	all = DedupeByURL(all)
	all = FilterOutInternships(all)
	all = FilterByLocation(all, params.Locations)
	return all, nil
}

// appleLocationFacetFor does a best-effort mapping from the free-text
// locations list to Apple's `location` search param. That param only
// accepts specific facet IDs (not arbitrary free text) - "india-INDC" was
// found by inspecting a live search request (2026-07-24) and is the only
// one this project needs; anything else is left unfiltered server-side and
// narrowed by FilterByLocation afterwards, same approach as amazon.go.
func appleLocationFacetFor(locations []string) string {
	for _, loc := range locations {
		if strings.EqualFold(strings.TrimSpace(loc), "india") {
			return "india-INDC"
		}
	}
	return ""
}

func (a *AppleScraper) searchOneRole(role, locationFacet string) ([]JobPosting, error) {
	endpoint := "https://jobs.apple.com/en-us/search"

	q := url.Values{}
	q.Set("search", role)
	q.Set("sort", "relevance")
	if locationFacet != "" {
		q.Set("location", locationFacet)
	}
	// Deliberately only fetches page 1 (~20 results) per role, same scope as
	// the Google scraper - good enough for this project's role list without
	// adding pagination-loop complexity/extra request volume.

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

	results, err := extractAppleSearchResults(string(body))
	if err != nil {
		return nil, err
	}

	jobs := make([]JobPosting, 0, len(results))
	for _, r := range results {
		jobs = append(jobs, appleResultToJobPosting(r))
	}
	return jobs, nil
}

// extractAppleSearchResults pulls the search results out of the page's
// hydration data blob. A page with zero matches for the query still parses
// fine here - searchResults just comes back as an empty slice - so unlike
// Google's scraper there's no separate "found the block but it's empty"
// case to special-case.
func extractAppleSearchResults(pageHTML string) ([]appleSearchResult, error) {
	m := appleHydrationRe.FindStringSubmatch(pageHTML)
	if m == nil {
		return nil, fmt.Errorf("could not find search results data block in Apple careers page (site structure may have changed)")
	}

	var payloadJSON string
	if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &payloadJSON); err != nil {
		return nil, fmt.Errorf("unescape hydration data: %w", err)
	}

	var payload appleHydrationPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("parse hydration data: %w", err)
	}

	return payload.LoaderData.Search.SearchResults, nil
}

func appleResultToJobPosting(r appleSearchResult) JobPosting {
	jobURL := fmt.Sprintf("https://jobs.apple.com/en-us/details/%s/%s", r.ID, r.TransformedPostingTitle)
	if r.Team.TeamCode != "" {
		jobURL += "?team=" + url.QueryEscape(r.Team.TeamCode)
	}

	locParts := make([]string, 0, len(r.Locations))
	for _, l := range r.Locations {
		parts := make([]string, 0, 3)
		for _, p := range []string{l.Name, l.StateProvince, l.CountryName} {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			locParts = append(locParts, strings.Join(parts, ", "))
		}
	}

	return JobPosting{
		Company:     "Apple",
		Title:       strings.TrimSpace(r.PostingTitle),
		URL:         jobURL,
		Location:    strings.Join(locParts, "; "),
		Description: strings.TrimSpace(r.JobSummary),
		PostedDate:  r.PostDateInGMT,
	}
}

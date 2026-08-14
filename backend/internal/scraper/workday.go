package scraper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WorkdayScraper talks to a Workday tenant's "CXS" JSON API - the same
// endpoint a Workday-hosted careers site calls to populate its own search
// results. Every tenant exposes an identical API shape, so this one scraper
// covers every company on the platform and only the tenant/shard/site triple
// changes. Verified live (2026-08-14) against all six sites constructed below.
type WorkdayScraper struct {
	Company string
	Tenant  string
	Shard   string // which "wdN" host the tenant lives on, e.g. "wd1"
	Site    string // the tenant's career site id, e.g. "nke"
}

func NewNikeScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "Nike", Tenant: "nike", Shard: "wd1", Site: "nke"}
}

func NewKLAScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "KLA", Tenant: "kla", Shard: "wd1", Site: "Search"}
}

// NewCiscoScraper targets Cisco's Workday tenant directly rather than
// careers.cisco.com. That public site is a Phenom front-end with its own
// searchable /widgets API, but its apply links all point back at this same
// Workday tenant - so this is the same job data with one less hop and one
// less response shape to maintain.
func NewCiscoScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "Cisco", Tenant: "cisco", Shard: "wd5", Site: "Cisco_Careers"}
}

// NewAdobeScraper targets Adobe's Workday tenant for the same reason as
// Cisco's - careers.adobe.com is a Phenom front-end over this tenant.
func NewAdobeScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "Adobe", Tenant: "adobe", Shard: "wd5", Site: "external_experienced"}
}

func NewSprinklrScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "Sprinklr", Tenant: "sprinklr", Shard: "wd1", Site: "careers"}
}

// NewRakutenScraper covers Rakuten Symphony only, which is a deliberate
// choice rather than an omission. Rakuten splits its openings across at
// least five separate career sites on one tenant (RakutenSymphony,
// RakutenMobile, RakutenAdvertising, RakutenAmericas, RakutenRewards) and
// as of 2026-08-14 Symphony was the only one with any India presence at all
// - it carries the Bengaluru/Indore engineering roles while the other four
// had zero India postings between them. If that changes, add another
// constructor and route rather than trying to make one cover all of them.
func NewRakutenScraper() *WorkdayScraper {
	return &WorkdayScraper{Company: "Rakuten", Tenant: "rakuten", Shard: "wd1", Site: "RakutenSymphony"}
}

const (
	// workdayPageLimit is Workday's hard maximum, not a preference: asking
	// for 21 or more returns HTTP 400.
	workdayPageLimit = 20

	// workdayMaxPagesPerRole bounds how deep we page for a single role.
	// Workday's searchText is fuzzy (KLA returns 543 of its 1014 postings for
	// "software engineer"), so the long tail of a query is mostly noise that
	// our own filters would drop anyway.
	workdayMaxPagesPerRole = 5

	// workdayDetailWorkers is how many job-detail fetches run at once. Kept
	// modest because they all hit a single tenant host.
	workdayDetailWorkers = 6

	// workdayIndiaCountryID is Workday's shared reference-data id for India,
	// which is the same value across tenants - see maybeIndiaFacet.
	workdayIndiaCountryID = "c4f78be1a8f14da0ab49ce1162348a5e"
)

// errWorkdayFacetRejected signals that a tenant refused our applied facets
// (rather than the request failing for an unrelated reason), so the caller
// can retry the same query unfaceted.
var errWorkdayFacetRejected = errors.New("workday: tenant rejected applied facets")

type workdayJobsResponse struct {
	Total       int                  `json:"total"`
	JobPostings []workdayJobListItem `json:"jobPostings"`
}

type workdayJobListItem struct {
	Title         string `json:"title"`
	ExternalPath  string `json:"externalPath"`
	LocationsText string `json:"locationsText"`
	TimeType      string `json:"timeType"`
	// PostedOn is a human string like "Posted Today" or "Posted 30+ Days
	// Ago", never a real date - the actual date only exists on the job
	// detail response as StartDate.
	PostedOn string `json:"postedOn"`
}

type workdayJobDetailResponse struct {
	JobPostingInfo struct {
		Title          string `json:"title"`
		JobDescription string `json:"jobDescription"`
		Location       string `json:"location"`
		StartDate      string `json:"startDate"`
		ExternalURL    string `json:"externalUrl"`
		JobReqID       string `json:"jobReqId"`
	} `json:"jobPostingInfo"`
}

func (w *WorkdayScraper) Search(params SearchParams) ([]JobPosting, error) {
	indiaFacet := w.wantsIndia(params.Locations)

	var candidates []JobPosting
	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := w.listRole(role, indiaFacet)
		if err != nil {
			return nil, fmt.Errorf("%s: role %q: %w", strings.ToLower(w.Company), role, err)
		}
		candidates = append(candidates, jobs...)
	}

	// Narrowing before the detail fetches is the whole reason this stays
	// cheap: the list response already carries title and location, so every
	// job dropped here is one fewer HTTP request in enrich(). Roles overlap
	// heavily under Workday's fuzzy search, so DedupeByURL alone removes a
	// large chunk.
	candidates = DedupeByURL(candidates)
	candidates = FilterOutInternships(candidates)
	candidates = FilterByLocation(candidates, params.Locations)

	return w.enrich(candidates)
}

// wantsIndia reports whether to try narrowing the query server-side to India.
//
// Tenants disagree on whether they honor this facet, which is why the result
// is only ever an optimization and never load-bearing: as of 2026-08-14 Adobe
// (317 -> 74 hits) and Sprinklr (44 -> 21) apply it correctly, Nike, Cisco
// and Rakuten silently ignore it and return their unfiltered totals, and KLA
// rejects it with HTTP 400. FilterByLocation is what actually guarantees
// correctness in all four cases; this just buys real coverage depth on the
// tenants that do respect it, since workdayMaxPagesPerRole is spent on
// India-only results instead of worldwide ones.
func (w *WorkdayScraper) wantsIndia(locations []string) bool {
	for _, loc := range locations {
		if strings.EqualFold(strings.TrimSpace(loc), "india") {
			return true
		}
	}
	return false
}

func (w *WorkdayScraper) listRole(role string, indiaFacet bool) ([]JobPosting, error) {
	var jobs []JobPosting

	for page := 0; page < workdayMaxPagesPerRole; page++ {
		if page > 0 {
			time.Sleep(250 * time.Millisecond)
		}

		resp, err := w.fetchPage(role, page*workdayPageLimit, indiaFacet)
		if err != nil {
			return nil, err
		}
		if len(resp.JobPostings) == 0 {
			break
		}

		for _, item := range resp.JobPostings {
			jobs = append(jobs, JobPosting{
				Company:  w.Company,
				Title:    strings.TrimSpace(item.Title),
				URL:      w.publicURL(item.ExternalPath),
				Location: strings.TrimSpace(item.LocationsText),
			})
		}

		if len(jobs) >= resp.Total {
			break
		}
	}

	return jobs, nil
}

// fetchPage runs one search request, retrying unfaceted if the tenant
// rejected the India facet.
func (w *WorkdayScraper) fetchPage(role string, offset int, indiaFacet bool) (*workdayJobsResponse, error) {
	if indiaFacet {
		resp, err := w.postSearch(role, offset, map[string][]string{
			"locationCountry": {workdayIndiaCountryID},
		})
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, errWorkdayFacetRejected) {
			return nil, err
		}
	}
	return w.postSearch(role, offset, map[string][]string{})
}

func (w *WorkdayScraper) postSearch(role string, offset int, facets map[string][]string) (*workdayJobsResponse, error) {
	payload, err := json.Marshal(map[string]any{
		"appliedFacets": facets,
		"limit":         workdayPageLimit,
		"offset":        offset,
		"searchText":    role,
	})
	if err != nil {
		return nil, err
	}

	req, err := newRequest(http.MethodPost, w.searchEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest && len(facets) > 0 {
		return nil, errWorkdayFacetRejected
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, w.searchEndpoint())
	}

	var parsed workdayJobsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	return &parsed, nil
}

// enrich fills in Description and PostedDate, neither of which the search
// response carries, via one detail request per job.
//
// A job whose detail request fails is kept with an empty description rather
// than dropped, so a flaky single request can't silently lose a real match -
// n8n's description-based filters fail open on empty input. But if every
// single detail request fails we surface an error instead, which is the
// signal that the endpoint shape changed rather than that today's search
// legitimately matched nothing.
func (w *WorkdayScraper) enrich(jobs []JobPosting) ([]JobPosting, error) {
	if len(jobs) == 0 {
		return jobs, nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures int
		lastErr  error
	)

	queue := make(chan int)
	for i := 0; i < workdayDetailWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				detail, err := w.fetchDetail(jobs[idx].URL)
				if err != nil {
					mu.Lock()
					failures++
					lastErr = err
					mu.Unlock()
					continue
				}
				jobs[idx].Description = stripHTML(detail.JobPostingInfo.JobDescription)
				jobs[idx].PostedDate = workdayPostedDate(detail.JobPostingInfo.StartDate)
				if loc := strings.TrimSpace(detail.JobPostingInfo.Location); loc != "" {
					jobs[idx].Location = loc
				}
			}
		}()
	}

	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	if failures == len(jobs) {
		return nil, fmt.Errorf("all %d job detail requests failed (site structure may have changed), last error: %w", failures, lastErr)
	}
	return jobs, nil
}

func (w *WorkdayScraper) fetchDetail(publicJobURL string) (*workdayJobDetailResponse, error) {
	req, err := newRequest(http.MethodGet, w.detailEndpoint(publicJobURL), nil)
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

	var parsed workdayJobDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode detail response: %w", err)
	}
	return &parsed, nil
}

// workdayPostedDate normalizes Workday's date-only startDate ("2026-08-14")
// into the RFC3339 timestamps every other scraper in this package emits, so
// n8n's staleness check sees one consistent format.
func workdayPostedDate(startDate string) string {
	startDate = strings.TrimSpace(startDate)
	if startDate == "" {
		return ""
	}
	parsed, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return startDate
	}
	return parsed.UTC().Format(time.RFC3339)
}

func (w *WorkdayScraper) baseURL() string {
	return fmt.Sprintf("https://%s.%s.myworkdayjobs.com", w.Tenant, w.Shard)
}

func (w *WorkdayScraper) searchEndpoint() string {
	return fmt.Sprintf("%s/wday/cxs/%s/%s/jobs", w.baseURL(), w.Tenant, w.Site)
}

// publicURL turns a search result's externalPath into the human-facing
// posting URL, which is what lands in the sheet.
func (w *WorkdayScraper) publicURL(externalPath string) string {
	if externalPath == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s%s", w.baseURL(), w.Site, externalPath)
}

// detailEndpoint maps a public posting URL back to its CXS detail endpoint.
// The two differ only by the /wday/cxs/{tenant} prefix ahead of the site id,
// so the externalPath is recoverable from the URL we already stored and
// doesn't need to be threaded through the filtering steps separately.
func (w *WorkdayScraper) detailEndpoint(publicJobURL string) string {
	externalPath := strings.TrimPrefix(publicJobURL, fmt.Sprintf("%s/%s", w.baseURL(), w.Site))
	return fmt.Sprintf("%s/wday/cxs/%s/%s%s", w.baseURL(), w.Tenant, w.Site, externalPath)
}

package scraper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func workdayRegistrations() []Registration {
	regs := []Registration{
		{Slug: "nike", Group: GroupCore, New: func() Scraper { return NewNikeScraper() }},
		{Slug: "kla", Group: GroupCore, New: func() Scraper { return NewKLAScraper() }},
		{Slug: "cisco", Group: GroupCore, New: func() Scraper { return NewCiscoScraper() }},
		{Slug: "adobe", Group: GroupCore, New: func() Scraper { return NewAdobeScraper() }},
		{Slug: "sprinklr", Group: GroupCore, New: func() Scraper { return NewSprinklrScraper() }},
		{Slug: "rakuten", Group: GroupCore, New: func() Scraper { return NewRakutenScraper() }},
	}

	// Tenants added 2026-08-24, each verified to answer the CXS search
	// endpoint. A table rather than a constructor apiece because config is
	// the only thing that differs between them.
	//
	// Cisco's entry above also covers Splunk: Splunk's careers site now
	// redirects into careers.cisco.com, which fronts this same tenant.
	for _, t := range []struct{ slug, company, tenant, shard, site string }{
		{"nvidia", "NVIDIA", "nvidia", "wd5", "NVIDIAExternalCareerSite"},
		{"salesforce", "Salesforce", "salesforce", "wd12", "External_Career_Site"},
		{"autodesk", "Autodesk", "autodesk", "wd1", "Ext"},
		{"marvell", "Marvell", "marvell", "wd1", "MarvellCareers"},
		{"samsung", "Samsung", "sec", "wd3", "Samsung_Careers"},
		{"broadcom", "Broadcom", "broadcom", "wd1", "External_Career"},
		{"visa", "Visa", "visa", "wd5", "Visa"},
		{"mastercard", "Mastercard", "mastercard", "wd1", "CorporateCareers"},
		{"crowdstrike", "CrowdStrike", "crowdstrike", "wd5", "crowdstrikecareers"},
		{"cadence", "Cadence", "cadence", "wd1", "External_Careers"},
		{"browserstack", "BrowserStack", "browserstack", "wd3", "External"},
		// HPE's tenant serves Juniper too, following the acquisition.
		{"hpe", "HPE", "hpe", "wd5", "Jobsathpe"},
		{"workdayinc", "Workday", "workday", "wd5", "Workday"},
	} {
		t := t
		regs = append(regs, Registration{Slug: t.slug, Group: GroupEnterprise, New: func() Scraper {
			return &WorkdayScraper{Company: t.company, Tenant: t.tenant, Shard: t.shard, Site: t.site}
		}})
	}
	return regs
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
	Facets      []workdayFacet       `json:"facets"`
}

type workdayFacet struct {
	FacetParameter string              `json:"facetParameter"`
	Values         []workdayFacetValue `json:"values"`
}

// workdayFacetValue is either a selectable leaf (Descriptor + ID) or a nested
// group carrying its own FacetParameter and Values. Location facets arrive
// wrapped that way: a "locationMainGroup" facet whose single value is the
// group that actually holds the per-place ids.
type workdayFacetValue struct {
	FacetParameter string              `json:"facetParameter"`
	Descriptor     string              `json:"descriptor"`
	ID             string              `json:"id"`
	Count          int                 `json:"count"`
	Values         []workdayFacetValue `json:"values"`
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
	facets := w.locationFacets(params.Locations)

	var candidates []JobPosting
	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := w.listRole(role, facets)
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

// wantsIndia reports whether to fall back to the shared India country id when
// a tenant advertises no location facet we can match.
//
// It is only a fallback because tenants disagree on the parameter name: as of
// 2026-08-24 Adobe, Sprinklr and Rakuten accept "locationCountry", Cisco and
// Nike want "locations" (per-city ids), and KLA wants "Country" and answers
// HTTP 400 to anything else. resolveLocationFacet asks each tenant instead of
// guessing. FilterByLocation is what guarantees correctness either way.
func (w *WorkdayScraper) wantsIndia(locations []string) bool {
	for _, loc := range locations {
		if strings.EqualFold(strings.TrimSpace(loc), "india") {
			return true
		}
	}
	return false
}

// locationFacets builds the appliedFacets payload that narrows a search to the
// requested places, preferring the tenant's own advertised location facet over
// the shared country id.
//
// This matters more than it looks: the page budget is spent on whatever the
// tenant returns, so on a tenant that ignores the facet we were paging through
// the first workdayMaxPagesPerRole*workdayPageLimit results of a worldwide
// list and throwing nearly all of them away in FilterByLocation. Cisco, for
// instance, reports 667 hits for "software" worldwide but only 200 in India -
// so an unfiltered search spends its whole budget mostly on US reqs.
func (w *WorkdayScraper) locationFacets(locations []string) map[string][]string {
	if param, ids := w.resolveLocationFacet(locations); len(ids) > 0 {
		return map[string][]string{param: ids}
	}
	if w.wantsIndia(locations) {
		return map[string][]string{"locationCountry": {workdayIndiaCountryID}}
	}
	return nil
}

// resolveLocationFacet asks the tenant which locations it can filter on and
// picks the ids matching the requested places. Facet ids are per-tenant, so
// they're discovered per run rather than hardcoded.
//
// Costs one extra request per scrape, which pays for itself immediately by not
// wasting the page budget on other countries.
func (w *WorkdayScraper) resolveLocationFacet(locations []string) (string, []string) {
	wanted := make(map[string]struct{}, len(locations))
	for _, loc := range locations {
		loc = strings.TrimSpace(loc)
		// "Remote" is not a geography. Matching it would select worldwide
		// remote reqs, which FilterByLocation then waves through on that same
		// word - so the facet would widen the search rather than narrow it.
		if loc == "" || strings.EqualFold(loc, "remote") {
			continue
		}
		wanted[strings.ToLower(loc)] = struct{}{}
	}
	if len(wanted) == 0 {
		return "", nil
	}

	// Empty searchText so the facet list covers the whole tenant rather than
	// only the places matching one role's results.
	resp, err := w.postSearch("", 0, nil)
	if err != nil {
		return "", nil
	}

	byParam := make(map[string][]string)
	for _, facet := range resp.Facets {
		collectWorkdayLocationIDs(facet.FacetParameter, facet.Values, wanted, byParam)
	}

	best, bestParam := 0, ""
	for param, ids := range byParam {
		if len(ids) > best {
			best, bestParam = len(ids), param
		}
	}
	if bestParam == "" {
		return "", nil
	}
	return bestParam, byParam[bestParam]
}

// collectWorkdayLocationIDs walks a facet tree, keeping ids whose descriptor
// names one of the requested places.
//
// Matching is per comma-separated component and exact, not substring: a
// descriptor like "Indianapolis, Indiana, US" must not match a request for
// "India", and "Remote - Indiana, USA" must not match "Remote".
func collectWorkdayLocationIDs(param string, values []workdayFacetValue, wanted map[string]struct{}, out map[string][]string) {
	for _, value := range values {
		childParam := param
		if value.FacetParameter != "" {
			childParam = value.FacetParameter
		}
		if len(value.Values) > 0 {
			collectWorkdayLocationIDs(childParam, value.Values, wanted, out)
			continue
		}
		if value.ID == "" || value.Descriptor == "" {
			continue
		}
		for _, part := range strings.Split(value.Descriptor, ",") {
			if _, ok := wanted[strings.ToLower(strings.TrimSpace(part))]; ok {
				out[childParam] = append(out[childParam], value.ID)
				break
			}
		}
	}
}

func (w *WorkdayScraper) listRole(role string, facets map[string][]string) ([]JobPosting, error) {
	var jobs []JobPosting

	for page := 0; page < workdayMaxPagesPerRole; page++ {
		if page > 0 {
			time.Sleep(250 * time.Millisecond)
		}

		resp, err := w.fetchPage(role, page*workdayPageLimit, facets)
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

// fetchPage runs one search request, retrying unfaceted if the tenant rejected
// the facets (KLA answers 400 rather than ignoring them).
func (w *WorkdayScraper) fetchPage(role string, offset int, facets map[string][]string) (*workdayJobsResponse, error) {
	if len(facets) > 0 {
		resp, err := w.postSearch(role, offset, facets)
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

	resp, err := w.doWithRetry(req, payload)
	if err != nil {
		return nil, err
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

// doWithRetry retries the transient statuses a Workday tenant returns under
// load. Worth having now that twenty tenants are queried per run rather than
// six: Broadcom answered 429 during the first full fan-out, and losing a whole
// company's results to one throttled request is a poor trade for a short wait.
//
// The request body is re-wrapped per attempt because a Reader is consumed by
// the first send, which is what makes retrying a POST safe here.
func (w *WorkdayScraper) doWithRetry(req *http.Request, payload []byte) (*http.Response, error) {
	const attempts = 3

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		retry := req.Clone(req.Context())
		retry.Body = io.NopCloser(bytes.NewReader(payload))

		resp, err := httpClient.Do(retry)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			resp.Body.Close()
			lastErr = fmt.Errorf("unexpected status %d from %s", resp.StatusCode, w.searchEndpoint())
			continue
		}
		return resp, nil
	}
	return nil, lastErr
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

package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MicrosoftScraper reads Microsoft's Eightfold-hosted careers site through
// /api/pcsx/search, which needs no authentication at all.
//
// This replaces an earlier documented stub, and the history is worth keeping
// because the obvious endpoint is a dead end: Eightfold's standard search API
// (/api/apply/v2/jobs) returns 403 "Not authorized for PCSX" for every plain
// HTTP request, and it still does so even when replaying a real browser's
// live x-csrf-token and cookies - verified 2026-08-14. So the 403 is not a
// CSRF or session problem and no amount of token minting fixes it.
//
// The way in was the site's own robots.txt, which explicitly allows
// "/api/pcsx". That path serves the same search backend with no token, no
// cookies and no CSRF header, and its result count matches the number the
// real UI displays for an identical query.
//
// Unlike every other scraper here, this one is built around the host's rate
// limiting rather than around its query surface. Fanning out one search per
// configured role got the whole scrape rejected with 429 on its very first
// request, and the throttle outlasted the run - three consecutive runs failed
// identically. So this scraper deliberately trades query breadth for request
// volume: one broad keyword, paced pagination, retries with backoff, capped
// concurrency, and partial results in preference to no results.
type MicrosoftScraper struct{}

func NewMicrosoftScraper() *MicrosoftScraper {
	return &MicrosoftScraper{}
}

const (
	microsoftBaseURL = "https://apply.careers.microsoft.com"

	// microsoftPageSize is fixed server-side at 10. Every override we tried
	// (num, pageSize, limit, size, num_per_page, rows) was ignored, so deeper
	// coverage costs proportionally more requests here than elsewhere.
	microsoftPageSize = 10

	// microsoftSearchQuery is one broad keyword covering all the configured
	// roles instead of a search per role. "software" against India returned
	// 151 postings on 2026-08-14, which subsumes the Software Engineer /
	// Backend Engineer / SDE / Software Developer variants the caller asks
	// for, and costs 8x fewer search requests than querying each one.
	//
	// The cost is that roles whose titles omit the word - SRE, Applied
	// Scientist - are not returned. That is an accepted trade: on this host,
	// a scrape that completes with most of the matches beats one that gets
	// 429'd and returns nothing.
	microsoftSearchQuery = "software"

	// microsoftMaxPages covers the full result set for the query above with
	// headroom, since only one query now runs instead of one per role.
	microsoftMaxPages = 18

	// microsoftDetailWorkers is deliberately low. Six parallel workers each
	// pulling a job page is what appears to have earned the 429 that then
	// poisoned subsequent runs.
	microsoftDetailWorkers = 2

	// microsoftDetailReadLimit caps how much of a job page we download. The
	// page is ~695 KB of single-page-app shell, but the JSON-LD block we want
	// ends by byte ~8.9 KB (verified 2026-08-14), so a bounded prefix read cuts
	// the transfer by more than an order of magnitude - which matters here
	// because bandwidth is part of what gets a client throttled.
	microsoftDetailReadLimit = 32 << 10

	microsoftMaxAttempts    = 4
	microsoftRetryBaseDelay = 2 * time.Second
	microsoftRetryJitter    = 500 * time.Millisecond

	// microsoftMaxRetryWait bounds what we'll honour from a Retry-After
	// header, so a hostile value can't stall the whole workflow.
	microsoftMaxRetryWait = 30 * time.Second

	microsoftPageDelay   = 900 * time.Millisecond
	microsoftDetailDelay = 400 * time.Millisecond
)

var microsoftLDJSONRe = regexp.MustCompile(`(?s)<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>`)

// microsoftSeniorKeywords mirrors the seniorKeywords list in the n8n "Filter
// Jobs" node, which is the authoritative seniority filter. Applying it here
// too is purely a volume measure: a broad keyword search returns a lot of
// Principal/Senior/Manager titles, and every one we drop before enrichment is
// one job page we don't fetch. Because the list is identical, nothing survives
// here that the workflow would have kept.
var microsoftSeniorKeywords = []string{
	"senior", " sr ", "sr.", "staff", "principal", "lead", "director",
	"manager", "head of", "architect", "iii", "l4", "l5", "l6", "l7",
	"ic4", "ic5", "ic6", "distinguished", "fellow", " vp ", "vice president",
	"chief", "group product manager",
}

type microsoftSearchResponse struct {
	Status int `json:"status"`
	Data   struct {
		Count     int                 `json:"count"`
		Positions []microsoftPosition `json:"positions"`
	} `json:"data"`
}

type microsoftPosition struct {
	ID           int64    `json:"id"`
	DisplayJobID string   `json:"displayJobId"`
	Name         string   `json:"name"`
	Locations    []string `json:"locations"`
	PositionURL  string   `json:"positionUrl"`
	Department   string   `json:"department"`
	// PostedTs and CreationTs are unix seconds. PostedTs is the one the UI
	// shows as "Posted N days ago" and is what we surface, so unlike the
	// Workday scraper there's no need to hit a detail endpoint for the date.
	PostedTs   int64 `json:"postedTs"`
	CreationTs int64 `json:"creationTs"`
}

// microsoftJobPostingLD is the schema.org JobPosting block Microsoft
// server-renders into each job page - the only place a description is
// available, since no JSON detail endpoint exists for a single position.
type microsoftJobPostingLD struct {
	Type        string `json:"@type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DatePosted  string `json:"datePosted"`
}

// Search ignores params.Roles by design - see microsoftSearchQuery - but still
// honours params.Locations, both as the API's own location filter and as the
// client-side narrowing every other scraper applies.
func (m *MicrosoftScraper) Search(params SearchParams) ([]JobPosting, error) {
	location := microsoftLocationFor(params.Locations)

	candidates, listErr := m.list(location)
	if len(candidates) == 0 {
		if listErr != nil {
			return nil, listErr
		}
		return nil, nil
	}

	// Same ordering rationale as the Workday scraper: the search response
	// already carries title, location and date, so filtering here is what
	// keeps the description fetches proportional to what we keep.
	candidates = DedupeByURL(candidates)
	candidates = FilterOutInternships(candidates)
	candidates = filterOutSeniorTitles(candidates)
	candidates = FilterByLocation(candidates, params.Locations)

	// listErr is intentionally dropped once anything came back: a page that
	// failed after its retries cost us coverage, not correctness, and the
	// caller logs the returned job count either way.
	return m.addDescriptions(candidates)
}

// microsoftLocationFor maps the free-text locations list onto the search
// API's `location` parameter, which takes a single place name. Only the case
// this project needs is handled, with FilterByLocation narrowing further
// afterwards - same approach as amazon.go and apple.go.
func microsoftLocationFor(locations []string) string {
	for _, loc := range locations {
		if strings.EqualFold(strings.TrimSpace(loc), "india") {
			return "india"
		}
	}
	return ""
}

// list pages through the search API, returning whatever it managed to collect
// alongside any error that stopped it early.
func (m *MicrosoftScraper) list(location string) ([]JobPosting, error) {
	var jobs []JobPosting

	for page := 0; page < microsoftMaxPages; page++ {
		if page > 0 {
			time.Sleep(microsoftPageDelay)
		}

		parsed, err := m.fetchPage(location, page*microsoftPageSize)
		if err != nil {
			return jobs, fmt.Errorf("microsoft: search page %d: %w", page, err)
		}
		if len(parsed.Data.Positions) == 0 {
			break
		}

		for _, p := range parsed.Data.Positions {
			jobs = append(jobs, JobPosting{
				Company:    "Microsoft",
				Title:      strings.TrimSpace(p.Name),
				URL:        m.jobURL(p),
				Location:   strings.Join(p.Locations, "; "),
				PostedDate: microsoftPostedDate(p),
			})
		}

		if parsed.Data.Count > 0 && len(jobs) >= parsed.Data.Count {
			break
		}
	}

	return jobs, nil
}

func (m *MicrosoftScraper) fetchPage(location string, start int) (*microsoftSearchResponse, error) {
	q := url.Values{}
	q.Set("domain", "microsoft.com")
	q.Set("query", microsoftSearchQuery)
	q.Set("start", strconv.Itoa(start))
	if location != "" {
		q.Set("location", location)
	}

	resp, err := microsoftGet(microsoftBaseURL+"/api/pcsx/search?"+q.Encode(), map[string]string{
		"Accept":  "application/json, text/plain, */*",
		"Referer": microsoftBaseURL + "/careers",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from search api", resp.StatusCode)
	}

	var parsed microsoftSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	return &parsed, nil
}

// microsoftGet issues a GET, retrying the transient statuses this host returns
// under load. It is local to this scraper on purpose: the other eleven
// scrapers have not needed retries, and giving them all one would change
// behaviour nobody has asked to change.
//
// The request is rebuilt per attempt rather than reused, which is what makes
// retrying a request safe.
func microsoftGet(endpoint string, headers map[string]string) (*http.Response, error) {
	var lastErr error
	delay := microsoftRetryBaseDelay

	for attempt := 1; attempt <= microsoftMaxAttempts; attempt++ {
		req, err := newRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}

		resp, err := httpClient.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case !microsoftRetryable(resp.StatusCode):
			return resp, nil
		default:
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			if wait := microsoftRetryAfter(resp); wait > delay {
				delay = wait
			}
			// Draining a little before closing lets the connection be reused
			// instead of torn down and redialled on the next attempt.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
		}

		if attempt == microsoftMaxAttempts {
			break
		}
		time.Sleep(delay + time.Duration(rand.Int63n(int64(microsoftRetryJitter))))
		delay *= 2
	}

	return nil, fmt.Errorf("after %d attempts: %w", microsoftMaxAttempts, lastErr)
}

// microsoftRetryable reports whether a status is worth another attempt. 429 is
// the one this host actually returns; the 5xx entries cover the load balancer
// shedding load in front of it.
func microsoftRetryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// microsoftRetryAfter reads a delay-seconds Retry-After header. The HTTP-date
// form is not handled because this host does not send it.
func microsoftRetryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return 0
	}
	if wait := time.Duration(seconds) * time.Second; wait < microsoftMaxRetryWait {
		return wait
	}
	return microsoftMaxRetryWait
}

func filterOutSeniorTitles(jobs []JobPosting) []JobPosting {
	filtered := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		// The padding reproduces the JS filter's ' ' + title + ' ' trick,
		// which is what lets " sr " and " vp " match as words.
		padded := " " + strings.ToLower(job.Title) + " "
		senior := false
		for _, keyword := range microsoftSeniorKeywords {
			if strings.Contains(padded, keyword) {
				senior = true
				break
			}
		}
		if !senior {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

// addDescriptions fills in Description from each job page's JSON-LD block.
//
// A job whose page fails is kept with an empty description rather than
// dropped, since n8n's description-based filters fail open and losing a real
// match is worse than passing one through unfiltered. If every page fails
// though, that's a structural change worth surfacing as an error.
func (m *MicrosoftScraper) addDescriptions(jobs []JobPosting) ([]JobPosting, error) {
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
	for i := 0; i < microsoftDetailWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				posting, err := m.fetchJobPostingLD(jobs[idx].URL)
				if err != nil {
					mu.Lock()
					failures++
					lastErr = err
					mu.Unlock()
				} else {
					jobs[idx].Description = stripHTML(posting.Description)
					if jobs[idx].PostedDate == "" {
						jobs[idx].PostedDate = posting.DatePosted
					}
				}
				// Pacing lives inside the worker so the delay applies per
				// request, making the effective rate workers/delay.
				time.Sleep(microsoftDetailDelay)
			}
		}()
	}

	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()

	if failures == len(jobs) {
		return nil, fmt.Errorf("all %d job page fetches failed (site structure may have changed), last error: %w", failures, lastErr)
	}
	return jobs, nil
}

func (m *MicrosoftScraper) fetchJobPostingLD(jobURL string) (*microsoftJobPostingLD, error) {
	resp, err := microsoftGet(jobURL, map[string]string{
		"Accept": "text/html,application/xhtml+xml",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, microsoftDetailReadLimit))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	for _, match := range microsoftLDJSONRe.FindAllStringSubmatch(string(body), -1) {
		var posting microsoftJobPostingLD
		if err := json.Unmarshal([]byte(match[1]), &posting); err != nil {
			continue
		}
		if posting.Type == "JobPosting" {
			return &posting, nil
		}
	}
	return nil, fmt.Errorf("no JobPosting JSON-LD block found in job page")
}

func (m *MicrosoftScraper) jobURL(p microsoftPosition) string {
	if p.PositionURL != "" {
		return microsoftBaseURL + p.PositionURL
	}
	return fmt.Sprintf("%s/careers/job/%d", microsoftBaseURL, p.ID)
}

func microsoftPostedDate(p microsoftPosition) string {
	ts := p.PostedTs
	if ts == 0 {
		ts = p.CreationTs
	}
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

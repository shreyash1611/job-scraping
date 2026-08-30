package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OracleScraper reads a careers site hosted on Oracle Recruiting Cloud's
// Candidate Experience API.
//
// This is how Uber became reachable. Investigated in July 2026, jobs.uber.com
// was a Next.js SPA with no server-rendered job data and an internal API that
// answered 403 or "missing CSRF token" to every direct call, so it was written
// off as needing a headless browser. Uber has since moved onto Oracle
// Recruiting, whose CE API takes no auth, no cookie and no token at all -
// verified 2026-08-24, 130 postings for "software" in a single request.
type OracleScraper struct {
	Company string
	// Host is the tenant's pod, e.g. "iaziqy.fa.ocs.oraclecloud.com".
	Host string
	// SiteNumber identifies the careers site within the tenant. Uber's pod
	// answers identically for every value tried, but it is a required
	// parameter so it stays configurable.
	SiteNumber string
	// SiteName is the human path segment in apply links, which is not
	// always the same as SiteNumber. Empty falls back to Company.
	SiteName string
}

const (
	// oracleListLimit is how many requisitions to pull per search. The API
	// accepts 200 in one response, which covers a full keyword search
	// without pagination.
	oracleListLimit = 200

	// oracleDetailWorkers keeps description fetches gentle; the list gives
	// no description, so one request per surviving job is unavoidable.
	oracleDetailWorkers = 4
)

type oracleListResponse struct {
	Items []struct {
		TotalJobsCount  int                     `json:"TotalJobsCount"`
		RequisitionList []oracleRequisitionItem `json:"requisitionList"`
	} `json:"items"`
}

type oracleRequisitionItem struct {
	ID               string `json:"Id"`
	Title            string `json:"Title"`
	PrimaryLocation  string `json:"PrimaryLocation"`
	PostedDate       string `json:"PostedDate"`
	ShortDescription string `json:"ShortDescriptionStr"`
}

type oracleDetailResponse struct {
	Items []struct {
		Title           string `json:"Title"`
		ExternalDesc    string `json:"ExternalDescriptionStr"`
		ExternalQuals   string `json:"ExternalQualificationsStr"`
		ExternalRespons string `json:"ExternalResponsibilitiesStr"`
	} `json:"items"`
}

func (o *OracleScraper) Search(params SearchParams) ([]JobPosting, error) {
	var candidates []JobPosting

	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(300 * time.Millisecond)
		}

		jobs, err := o.list(role)
		if err != nil {
			return nil, fmt.Errorf("%s: role %q: %w", strings.ToLower(o.Company), role, err)
		}
		candidates = append(candidates, jobs...)
	}

	// Narrow before spending a request per job on descriptions.
	candidates = DedupeByURL(candidates)
	candidates = FilterOutInternships(candidates)
	candidates = FilterByLocation(candidates, params.Locations)

	return o.enrich(candidates), nil
}

func (o *OracleScraper) list(role string) ([]JobPosting, error) {
	// The finder syntax is Oracle's own: a named finder plus comma-separated
	// arguments, all inside one query parameter.
	finder := fmt.Sprintf("findReqs;siteNumber=%s,limit=%d,keyword=%s,sortBy=POSTING_DATES_DESC",
		o.SiteNumber, oracleListLimit, role)

	q := url.Values{}
	q.Set("onlyData", "true")
	q.Set("expand", "requisitionList")
	q.Set("finder", finder)

	endpoint := fmt.Sprintf("https://%s/hcmRestApi/resources/latest/recruitingCEJobRequisitions?%s",
		o.Host, q.Encode())

	req, err := newRequest(http.MethodGet, endpoint, nil)
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

	var parsed oracleListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("search response carried no result block (API shape may have changed)")
	}

	items := parsed.Items[0].RequisitionList
	jobs := make([]JobPosting, 0, len(items))
	for _, item := range items {
		if item.ID == "" || item.Title == "" {
			continue
		}
		jobs = append(jobs, JobPosting{
			Company:  o.Company,
			Title:    strings.TrimSpace(item.Title),
			URL:      o.publicURL(item.ID),
			Location: strings.TrimSpace(item.PrimaryLocation),
			// Kept as a placeholder so a job whose detail fetch fails still
			// carries something; enrich overwrites it on success.
			Description: strings.TrimSpace(item.ShortDescription),
			PostedDate:  oraclePostedDate(item.PostedDate),
		})
	}
	return jobs, nil
}

// enrich fills in full descriptions, which the list response omits.
//
// A failed detail fetch leaves the short description in place rather than
// dropping the job, matching how the Workday scraper fails open.
func (o *OracleScraper) enrich(jobs []JobPosting) []JobPosting {
	if len(jobs) == 0 {
		return jobs
	}

	type result struct {
		index int
		desc  string
	}

	indexes := make(chan int)
	results := make(chan result)

	workers := oracleDetailWorkers
	if len(jobs) < workers {
		workers = len(jobs)
	}

	for w := 0; w < workers; w++ {
		go func() {
			for i := range indexes {
				desc, err := o.detail(jobs[i].URL)
				if err != nil {
					results <- result{index: i}
					continue
				}
				results <- result{index: i, desc: desc}
			}
		}()
	}

	go func() {
		for i := range jobs {
			indexes <- i
		}
		close(indexes)
	}()

	for range jobs {
		r := <-results
		if r.desc != "" {
			jobs[r.index].Description = r.desc
		}
	}
	return jobs
}

func (o *OracleScraper) detail(publicURL string) (string, error) {
	id := publicURL[strings.LastIndex(publicURL, "/")+1:]

	q := url.Values{}
	q.Set("onlyData", "true")
	q.Set("expand", "all")
	q.Set("finder", fmt.Sprintf("ById;Id=%s,siteNumber=%s", id, o.SiteNumber))

	endpoint := fmt.Sprintf("https://%s/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails?%s",
		o.Host, q.Encode())

	req, err := newRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed oracleDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Items) == 0 {
		return "", fmt.Errorf("no detail block")
	}

	item := parsed.Items[0]
	return stripHTML(strings.Join([]string{
		item.ExternalDesc, item.ExternalRespons, item.ExternalQuals,
	}, "\n")), nil
}

func (o *OracleScraper) publicURL(id string) string {
	return fmt.Sprintf("https://%s/hcmUI/CandidateExperience/en/sites/%s/job/%s",
		o.Host, o.siteName(), id)
}

func (o *OracleScraper) siteName() string {
	if o.SiteName != "" {
		return o.SiteName
	}
	return o.Company
}

func oraclePostedDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// The API returns a plain date, already the shape n8n wants.
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed.Format("2006-01-02")
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC().Format("2006-01-02")
	}
	return raw
}

func oracleRegistrations() []Registration {
	return []Registration{
		{Slug: "uber", Group: GroupEnterprise, New: func() Scraper {
			return &OracleScraper{
				Company:    "Uber",
				Host:       "iaziqy.fa.ocs.oraclecloud.com",
				SiteNumber: "CX_1",
				SiteName:   "UberCareers",
			}
		}},
		{Slug: "jpmorgan", Group: GroupIndia, New: func() Scraper {
			return &OracleScraper{
				Company:    "JP Morgan",
				Host:       "jpmc.fa.oraclecloud.com",
				SiteNumber: "CX_1001",
				SiteName:   "CX_1001",
			}
		}},
		{Slug: "dpworld", Group: GroupIndia, New: func() Scraper {
			return &OracleScraper{
				Company:    "DP World",
				Host:       "ehpv.fa.em2.oraclecloud.com",
				SiteNumber: "CX_1",
				SiteName:   "CX_1",
			}
		}},
	}
}

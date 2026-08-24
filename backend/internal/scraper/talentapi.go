package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TalentAPIScraper reads careers sites that expose the Google Cloud Talent
// style /api/jobs endpoint, which serves keyword and location filtering
// server-side and inlines the full description, responsibilities and
// qualifications on every hit.
//
// AMD is the one confirmed user of this shape. The pattern was tested against
// 21 other careers hosts on 2026-08-24 and every one of them either served
// HTML or refused the request, so this stays a one-entry family rather than
// the generic unlock it first looked like.
type TalentAPIScraper struct {
	Company string
	// Host is the careers hostname, e.g. "careers.amd.com".
	Host string
	// JobPathPrefix builds a human-visitable URL from a posting slug, since
	// the API's own apply_url points at a bare ATS login page.
	JobPathPrefix string
}

const (
	talentAPIPageSize = 100
	talentAPIMaxPages = 5
)

type talentAPIResponse struct {
	TotalCount int `json:"totalCount"`
	Jobs       []struct {
		Data talentAPIJob `json:"data"`
	} `json:"jobs"`
}

type talentAPIJob struct {
	Slug             string `json:"slug"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Responsibilities string `json:"responsibilities"`
	Qualifications   string `json:"qualifications"`
	FullLocation     string `json:"full_location"`
	LocationName     string `json:"location_name"`
	Country          string `json:"country"`
	PostedDate       string `json:"posted_date"`
	ApplyURL         string `json:"apply_url"`
	EmploymentType   string `json:"employment_type"`
}

func (t *TalentAPIScraper) Search(params SearchParams) ([]JobPosting, error) {
	var all []JobPosting

	for i, role := range params.Roles {
		if i > 0 {
			time.Sleep(400 * time.Millisecond)
		}

		jobs, err := t.searchOneRole(role, params.Locations)
		if err != nil {
			return nil, fmt.Errorf("%s: role %q: %w", strings.ToLower(t.Company), role, err)
		}
		all = append(all, jobs...)
	}

	all = DedupeByURL(all)
	all = FilterOutInternships(all)
	all = FilterByLocation(all, params.Locations)
	return all, nil
}

func (t *TalentAPIScraper) searchOneRole(role string, locations []string) ([]JobPosting, error) {
	var jobs []JobPosting

	for page := 1; page <= talentAPIMaxPages; page++ {
		if page > 1 {
			time.Sleep(250 * time.Millisecond)
		}

		resp, err := t.fetchPage(role, locations, page)
		if err != nil {
			return nil, err
		}
		if len(resp.Jobs) == 0 {
			break
		}

		for _, entry := range resp.Jobs {
			job := entry.Data
			if job.Title == "" {
				continue
			}
			jobs = append(jobs, JobPosting{
				Company:  t.Company,
				Title:    strings.TrimSpace(job.Title),
				URL:      t.jobURL(job),
				Location: talentAPILocation(job),
				Description: stripHTML(strings.Join([]string{
					job.Description, job.Responsibilities, job.Qualifications,
				}, "\n")),
				PostedDate: talentAPIPostedDate(job.PostedDate),
			})
		}

		if len(jobs) >= resp.TotalCount {
			break
		}
	}

	return jobs, nil
}

func (t *TalentAPIScraper) fetchPage(role string, locations []string, page int) (*talentAPIResponse, error) {
	q := url.Values{}
	q.Set("keyword", role)
	q.Set("page", fmt.Sprint(page))
	q.Set("limit", fmt.Sprint(talentAPIPageSize))
	q.Set("sortBy", "relevance")
	// One geocodable hint only, same constraint as Google careers; the real
	// narrowing across every requested spelling is FilterByLocation's job.
	if hint := googleLocationHint(locations); hint != "" {
		q.Set("location", hint)
	}

	endpoint := fmt.Sprintf("https://%s/api/jobs?%s", t.Host, q.Encode())

	req, err := newRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// This host 403s a request that arrives with no referer.
	req.Header.Set("Referer", "https://"+t.Host+"/")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed talentAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	return &parsed, nil
}

func (t *TalentAPIScraper) jobURL(job talentAPIJob) string {
	if job.Slug != "" && t.JobPathPrefix != "" {
		return "https://" + t.Host + t.JobPathPrefix + job.Slug
	}
	return strings.TrimSpace(job.ApplyURL)
}

// talentAPILocation prefers full_location ("Hyderabad, India") over
// location_name, which arrives in an internal format ("IN,Hyderabad-Design
// Center") that reads badly on the sheet.
func talentAPILocation(job talentAPIJob) string {
	if loc := strings.TrimSpace(job.FullLocation); loc != "" {
		return loc
	}
	if loc := strings.TrimSpace(job.LocationName); loc != "" {
		return loc
	}
	return strings.TrimSpace(job.Country)
}

func talentAPIPostedDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC().Format("2006-01-02")
		}
	}
	return raw
}

func talentAPIRegistrations() []Registration {
	return []Registration{
		{Slug: "amd", Group: GroupEnterprise, New: func() Scraper {
			return &TalentAPIScraper{
				Company:       "AMD",
				Host:          "careers.amd.com",
				JobPathPrefix: "/careers-home/jobs/",
			}
		}},
	}
}

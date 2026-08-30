package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SmartRecruitersScraper reads a company's public SmartRecruiters posting
// API. PhonePe left Greenhouse for this sometime after 2026-08-24: the
// boards-api.greenhouse.io/phonepe slug now 404s, while
// jobs.smartrecruiters.com/PHONEPELIMITED is what their careers page points
// at (via www.phonepe.com/apollo/job-postings/latest.json).
type SmartRecruitersScraper struct {
	Company    string
	Identifier string
}

const smartRecruitersPageLimit = 100

type smartRecruitersListResponse struct {
	Offset     int                      `json:"offset"`
	TotalFound int                      `json:"totalFound"`
	Content    []smartRecruitersListJob `json:"content"`
}

type smartRecruitersListJob struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	ReleasedDate string                  `json:"releasedDate"`
	Location     smartRecruitersLocation `json:"location"`
	Ref          string                  `json:"ref"`
}

type smartRecruitersLocation struct {
	City         string `json:"city"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	FullLocation string `json:"fullLocation"`
	Remote       bool   `json:"remote"`
}

type smartRecruitersDetail struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	ReleasedDate string                  `json:"releasedDate"`
	PostingURL   string                  `json:"postingUrl"`
	Location     smartRecruitersLocation `json:"location"`
	JobAd        struct {
		Sections map[string]struct {
			Text string `json:"text"`
		} `json:"sections"`
	} `json:"jobAd"`
}

func (s *SmartRecruitersScraper) Search(params SearchParams) ([]JobPosting, error) {
	listed, err := s.list()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.ToLower(s.Company), err)
	}

	candidates := make([]JobPosting, 0, len(listed))
	for _, item := range listed {
		if !titleMatchesAnyRole(item.Name, params.Roles) {
			continue
		}
		candidates = append(candidates, JobPosting{
			Company:    s.Company,
			Title:      strings.TrimSpace(item.Name),
			URL:        s.publicURL(item.ID),
			Location:   smartRecruitersLocationText(item.Location),
			PostedDate: smartRecruitersPostedDate(item.ReleasedDate),
		})
	}

	candidates = DedupeByURL(candidates)
	candidates = FilterOutInternships(candidates)
	candidates = FilterByLocation(candidates, params.Locations)

	return s.enrich(candidates)
}

func (s *SmartRecruitersScraper) list() ([]smartRecruitersListJob, error) {
	var all []smartRecruitersListJob
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", fmt.Sprint(smartRecruitersPageLimit))
		q.Set("offset", fmt.Sprint(offset))
		endpoint := fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings?%s",
			s.Identifier, q.Encode())

		req, err := newRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, fmt.Errorf("smartrecruiters company %q does not exist", s.Identifier)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		var parsed smartRecruitersListResponse
		err = json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode list response: %w", err)
		}

		if len(parsed.Content) == 0 {
			break
		}
		all = append(all, parsed.Content...)
		offset += len(parsed.Content)
		if offset >= parsed.TotalFound {
			break
		}
	}

	return all, nil
}

func (s *SmartRecruitersScraper) enrich(jobs []JobPosting) ([]JobPosting, error) {
	for i := range jobs {
		if i > 0 {
			time.Sleep(150 * time.Millisecond)
		}
		detail, err := s.detail(jobs[i].URL)
		if err != nil {
			continue
		}
		if detail.PostingURL != "" {
			jobs[i].URL = detail.PostingURL
		}
		jobs[i].Description = smartRecruitersDescription(detail)
		if loc := smartRecruitersLocationText(detail.Location); loc != "" {
			jobs[i].Location = loc
		}
	}
	return jobs, nil
}

func (s *SmartRecruitersScraper) detail(publicURL string) (*smartRecruitersDetail, error) {
	id := publicURL[strings.LastIndex(publicURL, "/")+1:]
	endpoint := fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings/%s",
		s.Identifier, id)

	req, err := newRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed smartRecruitersDetail
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *SmartRecruitersScraper) publicURL(id string) string {
	return fmt.Sprintf("https://jobs.smartrecruiters.com/%s/%s", s.Identifier, id)
}

func smartRecruitersLocationText(loc smartRecruitersLocation) string {
	if loc.Remote && strings.TrimSpace(loc.FullLocation) == "" {
		return "Remote"
	}
	if loc.FullLocation != "" {
		if loc.Remote && !strings.Contains(strings.ToLower(loc.FullLocation), "remote") {
			return loc.FullLocation + "; Remote"
		}
		return loc.FullLocation
	}
	parts := make([]string, 0, 3)
	for _, p := range []string{loc.City, loc.Region, loc.Country} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, strings.TrimSpace(p))
		}
	}
	return strings.Join(parts, ", ")
}

func smartRecruitersDescription(detail *smartRecruitersDetail) string {
	order := []string{"jobDescription", "qualifications", "additionalInformation", "companyDescription"}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		section, ok := detail.JobAd.Sections[key]
		if !ok || strings.TrimSpace(section.Text) == "" {
			continue
		}
		parts = append(parts, stripHTML(section.Text))
	}
	return strings.Join(parts, "\n\n")
}

func smartRecruitersPostedDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return parsed.UTC().Format("2006-01-02")
}

func smartRecruitersRegistrations() []Registration {
	boards := []struct {
		slug, company, ident string
		group                int
	}{
		{"phonepe", "PhonePe", "PHONEPELIMITED", GroupProduct},
		{"swiggy", "Swiggy", "swiggy", GroupIndia},
		{"freshworks", "Freshworks", "freshworks", GroupIndia},
		{"ixigo", "ixigo", "ixigo", GroupIndia},
	}
	regs := make([]Registration, 0, len(boards))
	for _, b := range boards {
		b := b
		regs = append(regs, Registration{Slug: b.slug, Group: b.group, New: func() Scraper {
			return &SmartRecruitersScraper{Company: b.company, Identifier: b.ident}
		}})
	}
	return regs
}

package scraper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// LeverScraper reads a company's public Lever postings API. One unauthenticated
// request returns every live posting with a plain-text description, so there
// is no pagination and no per-job detail fetch.
type LeverScraper struct {
	Company   string
	BoardSlug string
}

// leverTechTitleRe covers Indian-board titles that never say "software
// engineer" - Fi posts "Member of Technical Staff (MTS)", Meesho-style SDE
// titles are already caught by the role words.
var leverTechTitleRe = regexp.MustCompile(`(?i)\b(mts|sde|full-?stack|member of technical staff)\b`)

type leverPosting struct {
	Text             string `json:"text"`
	HostedURL        string `json:"hostedUrl"`
	CreatedAt        int64  `json:"createdAt"`
	DescriptionPlain string `json:"descriptionPlain"`
	Description      string `json:"description"`
	Categories       struct {
		Location   string `json:"location"`
		Commitment string `json:"commitment"`
	} `json:"categories"`
	Lists []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
}

func (l *LeverScraper) Search(params SearchParams) ([]JobPosting, error) {
	postings, err := l.fetchBoard()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.ToLower(l.Company), err)
	}
	if len(postings) == 0 {
		return nil, fmt.Errorf("%s: lever board %q returned no postings at all (board slug may have changed)",
			strings.ToLower(l.Company), l.BoardSlug)
	}

	jobs := make([]JobPosting, 0, len(postings))
	for _, p := range postings {
		title := strings.TrimSpace(p.Text)
		if !titleMatchesAnyRole(title, params.Roles) && !leverTechTitleRe.MatchString(title) {
			continue
		}
		jobs = append(jobs, JobPosting{
			Company:     l.Company,
			Title:       strings.TrimSpace(p.Text),
			URL:         strings.TrimSpace(p.HostedURL),
			Location:    strings.TrimSpace(p.Categories.Location),
			Description: leverDescription(p),
			PostedDate:  leverPostedDate(p.CreatedAt),
		})
	}

	jobs = DedupeByURL(jobs)
	jobs = FilterOutInternships(jobs)
	jobs = FilterByLocation(jobs, params.Locations)
	return jobs, nil
}

func (l *LeverScraper) fetchBoard() ([]leverPosting, error) {
	endpoint := fmt.Sprintf("https://api.lever.co/v0/postings/%s?mode=json", l.BoardSlug)
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
		return nil, fmt.Errorf("lever board %q does not exist", l.BoardSlug)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var parsed []leverPosting
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode board response: %w", err)
	}
	return parsed, nil
}

func leverDescription(p leverPosting) string {
	parts := make([]string, 0, 2+len(p.Lists))
	if strings.TrimSpace(p.DescriptionPlain) != "" {
		parts = append(parts, strings.TrimSpace(p.DescriptionPlain))
	} else if strings.TrimSpace(p.Description) != "" {
		parts = append(parts, stripHTML(p.Description))
	}
	for _, item := range p.Lists {
		block := strings.TrimSpace(item.Text)
		body := stripHTML(item.Content)
		if body != "" {
			if block != "" {
				block += "\n" + body
			} else {
				block = body
			}
		}
		if block != "" {
			parts = append(parts, block)
		}
	}
	return strings.Join(parts, "\n\n")
}

func leverPostedDate(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

func leverRegistrations() []Registration {
	boards := []struct{ slug, company, board string }{
		{"meesho", "Meesho", "meesho"},
		{"cred", "CRED", "cred"},
		{"paytm", "Paytm", "paytm"},
		{"epifi", "Fi", "epifi"},
	}
	regs := make([]Registration, 0, len(boards))
	for _, b := range boards {
		b := b
		regs = append(regs, Registration{Slug: b.slug, Group: GroupIndia, New: func() Scraper {
			return &LeverScraper{Company: b.company, BoardSlug: b.board}
		}})
	}
	return regs
}

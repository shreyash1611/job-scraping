package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DEShawScraper reads D. E. Shaw India's careers page. The listing is a
// Next.js app that server-renders every regular (external) opening into
// window.__NEXT_DATA__, so one GET is enough - there is no public REST
// search, and the Greenhouse/Lever slugs do not resolve.
type DEShawScraper struct{}

var nextDataRe = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

var deshawTechTitleRe = regexp.MustCompile(`(?i)\b(tech|developer|sde|software)\b`)

type deshawNextData struct {
	Props struct {
		PageProps struct {
			RegularJobs []deshawJob `json:"regularJobs"`
		} `json:"pageProps"`
	} `json:"props"`
}

type deshawJob struct {
	ID          int    `json:"id"`
	DisplayName string `json:"displayName"`
	Office      []struct {
		Name string `json:"name"`
	} `json:"office"`
	Data struct {
		JobURL      string `json:"jobUrl"`
		Description struct {
			WebsiteDescription       string     `json:"websiteDescription"`
			Responsibilities         string     `json:"responsibilities"`
			PeopleWeAreLookingFor    stringList `json:"peopleWeAreLookingFor"`
			PeopleWeAreLookingForStr string     `json:"peopleWeAreLookingForStr"`
		} `json:"jobDescription"`
	} `json:"data"`
}

func (d *DEShawScraper) Search(params SearchParams) ([]JobPosting, error) {
	jobs, err := d.fetch()
	if err != nil {
		return nil, fmt.Errorf("deshaw: %w", err)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("deshaw: careers page returned no regular openings (page shape may have changed)")
	}

	out := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		title := strings.TrimSpace(job.DisplayName)
		if title == "" {
			continue
		}
		if !titleMatchesAnyRole(title, params.Roles) && !deshawTechTitleRe.MatchString(title) {
			continue
		}
		url := strings.TrimSpace(job.Data.JobURL)
		if url == "" {
			continue
		}
		if !strings.HasPrefix(url, "http") {
			url = "https://www.deshawindia.com/careers/" + strings.TrimPrefix(url, "/")
		}
		out = append(out, JobPosting{
			Company:     "D.E. Shaw",
			Title:       title,
			URL:         url,
			Location:    deshawLocation(job),
			Description: deshawDescription(job),
		})
	}

	out = DedupeByURL(out)
	out = FilterOutInternships(out)
	out = FilterByLocation(out, params.Locations)
	return out, nil
}

func (d *DEShawScraper) fetch() ([]deshawJob, error) {
	req, err := newRequest(http.MethodGet, "https://www.deshawindia.com/careers/work-with-us", nil)
	if err != nil {
		return nil, err
	}
	// The careers page embeds the full board in __NEXT_DATA__, so the
	// payload is a couple of megabytes. The shared 20s client is tight.
	client := *httpClient
	client.Timeout = 45 * time.Second
	resp, err := client.Do(req)
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

	match := nextDataRe.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("could not find __NEXT_DATA__ on careers page")
	}

	var parsed deshawNextData
	if err := json.Unmarshal(match[1], &parsed); err != nil {
		return nil, fmt.Errorf("decode next data: %w", err)
	}
	return parsed.Props.PageProps.RegularJobs, nil
}

func deshawLocation(job deshawJob) string {
	parts := make([]string, 0, len(job.Office))
	for _, office := range job.Office {
		if name := strings.TrimSpace(office.Name); name != "" {
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return "India"
	}
	return strings.Join(parts, "; ") + ", India"
}

func deshawDescription(job deshawJob) string {
	desc := job.Data.Description
	chunks := []string{
		desc.WebsiteDescription,
		desc.Responsibilities,
		string(desc.PeopleWeAreLookingFor),
		desc.PeopleWeAreLookingForStr,
	}
	parts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if t := strings.TrimSpace(stripHTML(c)); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func deshawRegistrations() []Registration {
	return []Registration{
		{Slug: "deshaw", Group: GroupIndia, New: func() Scraper { return &DEShawScraper{} }},
	}
}

// stringList accepts a JSON string, a JSON array of strings, or null.
// D. E. Shaw's careers page uses all three shapes for peopleWeAreLookingFor.
type stringList string

func (s *stringList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = stringList(str)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*s = stringList(strings.Join(arr, "\n"))
		return nil
	}
	*s = ""
	return nil
}

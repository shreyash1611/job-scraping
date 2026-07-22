package scraper

// JobPosting is the normalized shape returned by every scraper, matching the
// contract the n8n workflow's mock nodes already produce.
type JobPosting struct {
	Company     string `json:"company"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Location    string `json:"location"`
	Description string `json:"description"`
	PostedDate  string `json:"postedDate"`
}

// SearchParams holds the role/location filters passed in from the caller
// (n8n's Search Config node).
type SearchParams struct {
	Roles     []string
	Locations []string
}

// Scraper is implemented by each per-company scraper.
type Scraper interface {
	Search(params SearchParams) ([]JobPosting, error)
}

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"n8n-job-scraper/backend/internal/scraper"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /scrape/google", handleScrape(scraper.NewGoogleScraper()))
	mux.HandleFunc("GET /scrape/amazon", handleScrape(scraper.NewAmazonScraper()))
	mux.HandleFunc("GET /scrape/microsoft", handleScrape(scraper.NewMicrosoftScraper()))
	mux.HandleFunc("GET /scrape/apple", handleScrape(scraper.NewAppleScraper()))
	mux.HandleFunc("GET /scrape/uber", handleScrape(scraper.NewUberScraper()))

	// All Workday-hosted careers sites, served by one shared scraper.
	mux.HandleFunc("GET /scrape/nike", handleScrape(scraper.NewNikeScraper()))
	mux.HandleFunc("GET /scrape/kla", handleScrape(scraper.NewKLAScraper()))
	mux.HandleFunc("GET /scrape/cisco", handleScrape(scraper.NewCiscoScraper()))
	mux.HandleFunc("GET /scrape/adobe", handleScrape(scraper.NewAdobeScraper()))
	mux.HandleFunc("GET /scrape/sprinklr", handleScrape(scraper.NewSprinklrScraper()))
	mux.HandleFunc("GET /scrape/rakuten", handleScrape(scraper.NewRakutenScraper()))

	addr := ":8080"
	log.Printf("job scraper backend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleScrape wraps a scraper.Scraper into an HTTP handler that parses
// ?roles=&locations= query params and returns a JSON array of JobPosting.
func handleScrape(s scraper.Scraper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		params := scraper.SearchParams{
			Roles:     query["roles"],
			Locations: query["locations"],
		}

		if len(params.Roles) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "at least one 'roles' query parameter is required",
			})
			return
		}

		start := time.Now()
		jobs, err := s.Search(params)
		elapsed := time.Since(start)

		if err != nil {
			log.Printf("scrape error (%s): %v", elapsed, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": err.Error(),
			})
			return
		}

		log.Printf("scrape ok: %d jobs in %s", len(jobs), elapsed)
		writeJSON(w, http.StatusOK, jobs)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"n8n-job-scraper/backend/internal/scraper"
)

// fanOutConcurrency bounds how many company scrapes run at once.
//
// Kept well below the number of registered companies on purpose. These are
// independent hosts, so the ceiling is this machine's outbound connections and
// the risk of looking like a burst of automated traffic - not any one site's
// patience. Each scraper already paces its own internal requests.
const fanOutConcurrency = 8

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)

	registry := scraper.Registry()
	for _, reg := range registry {
		// Per-company routes stay available for debugging a single site,
		// even though the workflow now calls /scrape/all.
		mux.HandleFunc("GET /scrape/"+reg.Slug, handleScrape(reg.New()))
	}
	mux.HandleFunc("GET /scrape/all", handleScrapeAll(registry))

	// PORT is honoured so a second instance can be run alongside the live
	// one to test a change without stopping it.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	log.Printf("job scraper backend listening on %s with %d companies registered", addr, len(registry))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// scrapeFailure describes one company's failure in a fan-out response.
type scrapeFailure struct {
	Company string `json:"company"`
	Error   string `json:"error"`
}

// fanOutResponse is what /scrape/all returns.
//
// Jobs and errors travel together in one response so that a single failing
// company cannot fail the whole scrape - which is exactly what the old
// one-node-per-company workflow needed a separate error branch per company to
// achieve.
type fanOutResponse struct {
	Jobs      []scraper.JobPosting `json:"jobs"`
	Errors    []scrapeFailure      `json:"errors"`
	Attempted int                  `json:"attempted"`
	Succeeded int                  `json:"succeeded"`
	ElapsedMS int64                `json:"elapsedMs"`
}

// handleScrapeAll runs every registered scraper concurrently and returns one
// merged job list plus a per-company error list.
func handleScrapeAll(registry []scraper.Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, ok := parseParams(w, r)
		if !ok {
			return
		}

		selected := registry
		if raw := r.URL.Query().Get("group"); raw != "" {
			group, err := strconv.Atoi(raw)
			if err != nil || group < 1 || group > 3 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "group must be 1 (core), 2 (enterprise) or 3 (product)",
				})
				return
			}
			selected = scraper.RegistryForGroup(group)
			if len(selected) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "no companies registered in that group",
				})
				return
			}
		}

		start := time.Now()

		var (
			mu       sync.Mutex
			jobs     []scraper.JobPosting
			failures []scrapeFailure
			wg       sync.WaitGroup
		)

		slots := make(chan struct{}, fanOutConcurrency)

		for _, reg := range selected {
			reg := reg
			wg.Add(1)
			go func() {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()

				// A panic in one scraper would otherwise take down the whole
				// process and with it every other company's results.
				defer func() {
					if p := recover(); p != nil {
						mu.Lock()
						failures = append(failures, scrapeFailure{
							Company: reg.Slug,
							Error:   "panic during scrape",
						})
						mu.Unlock()
						log.Printf("scrape panic (%s): %v", reg.Slug, p)
					}
				}()

				found, err := reg.New().Search(params)

				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					failures = append(failures, scrapeFailure{Company: reg.Slug, Error: err.Error()})
					return
				}
				jobs = append(jobs, found...)
			}()
		}

		wg.Wait()

		// Stable ordering so a diff between two runs reflects real changes
		// rather than goroutine scheduling.
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].Company != jobs[j].Company {
				return jobs[i].Company < jobs[j].Company
			}
			return jobs[i].URL < jobs[j].URL
		})
		sort.Slice(failures, func(i, j int) bool { return failures[i].Company < failures[j].Company })

		elapsed := time.Since(start)
		log.Printf("scrape all: %d jobs from %d/%d companies in %s",
			len(jobs), len(selected)-len(failures), len(selected), elapsed)
		for _, f := range failures {
			log.Printf("  failed: %-14s %s", f.Company, f.Error)
		}

		if jobs == nil {
			jobs = []scraper.JobPosting{}
		}
		if failures == nil {
			failures = []scrapeFailure{}
		}

		writeJSON(w, http.StatusOK, fanOutResponse{
			Jobs:      jobs,
			Errors:    failures,
			Attempted: len(selected),
			Succeeded: len(selected) - len(failures),
			ElapsedMS: elapsed.Milliseconds(),
		})
	}
}

// handleScrape wraps a scraper.Scraper into an HTTP handler that parses
// ?roles=&locations= query params and returns a JSON array of JobPosting.
func handleScrape(s scraper.Scraper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		params, ok := parseParams(w, r)
		if !ok {
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

func parseParams(w http.ResponseWriter, r *http.Request) (scraper.SearchParams, bool) {
	query := r.URL.Query()
	params := scraper.SearchParams{
		Roles:     query["roles"],
		Locations: query["locations"],
	}

	if len(params.Roles) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "at least one 'roles' query parameter is required",
		})
		return params, false
	}
	return params, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

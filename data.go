package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Result is what a query returned.
type Result struct {
	Columns []string
	Rows    [][]any
	Err     error
	At      time.Time
	Stale   bool // Err is set but Rows are from an earlier successful run
}

// Source runs HogQL queries. PostHog is the only one for now; demo mode needs
// no source at all.
type Source interface {
	Run(q string) (columns []string, rows [][]any, err error)
}

type posthogSource struct {
	cfg  PostHog
	http *http.Client
}

func (p *posthogSource) Run(sql string) ([]string, [][]any, error) {
	if p.cfg.APIKey == "" || p.cfg.Project == "" {
		return nil, nil, errors.New("posthog.project and posthog.api_key are not set")
	}
	body, _ := json.Marshal(map[string]any{
		"name":  "amber",
		"query": map[string]any{"kind": "HogQLQuery", "query": sql},
	})
	req, err := http.NewRequest("POST", p.cfg.Host+"/api/projects/"+p.cfg.Project+"/query/", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Detail != "" {
			return nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Detail)
		}
		return nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	var out struct {
		Columns []string `json:"columns"`
		Results [][]any  `json:"results"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, nil, err
	}
	return out.Columns, out.Results, nil
}

// Store caches results per query, so widgets with a long cache_minutes (daily
// App Store reports, say) are not re-queried on every refresh, and a failed query
// keeps showing its last good data.
type Store struct {
	mu      sync.Mutex
	results map[string]*Result
}

func newStore() *Store { return &Store{results: map[string]*Result{}} }

type queryJob struct {
	sql   string
	cache time.Duration
	shape string // what the widget expects back; used by demo mode
}

// Stats describes one fetch round, for the footer.
type Stats struct {
	At      time.Time
	Took    time.Duration
	Ran     int
	Cached  int
	Failed  int
	LastErr string
}

// Fetch runs every query the screen needs that is not fresh in the cache: younger
// than the widget's cache_minutes, or than minAge, which lets previews reuse what
// the screen just fetched. A nil source means demo mode.
func (s *Store) Fetch(cfg *Config, src Source, force bool, minAge time.Duration) Stats {
	st := Stats{At: time.Now()}
	jobs := map[string]queryJob{}
	add := func(q Query, cacheMin int, shape string) {
		if q == "" {
			return
		}
		sql := cfg.expand(q)
		if j, ok := jobs[sql]; !ok || time.Duration(cacheMin)*time.Minute < j.cache {
			jobs[sql] = queryJob{sql, time.Duration(cacheMin) * time.Minute, shape}
		}
	}
	var walk func(w Widget)
	walk = func(w Widget) {
		shape := w.Type
		if w.Type == "chart" && w.Style == "columns" {
			shape = "chart:columns"
		}
		if w.Type == "bars" && w.Format != "money" {
			shape = "bars:list"
		}
		add(w.Query, w.CacheMinutes, shape)
		add(w.Series, w.CacheMinutes, "series")
		for _, it := range w.Items {
			walk(it)
		}
	}
	for _, r := range cfg.Rows {
		add(r.MetaQuery, 0, "meta")
		for _, w := range r.Cells {
			walk(w)
		}
	}
	if src == nil {
		s.mu.Lock()
		for _, j := range jobs {
			s.results[j.sql] = demoResult(j)
		}
		s.mu.Unlock()
		st.Ran = len(jobs)
		st.Took = time.Since(st.At)
		return st
	}

	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, j := range jobs {
		s.mu.Lock()
		old := s.results[j.sql]
		s.mu.Unlock()
		if !force && old != nil && old.Err == nil && time.Since(old.At) < max(j.cache, minAge) {
			st.Cached++
			continue
		}
		st.Ran++
		wg.Add(1)
		go func(j queryJob, old *Result) {
			defer wg.Done()
			sem <- struct{}{}
			cols, rows, err := src.Run(j.sql)
			<-sem
			r := &Result{Columns: cols, Rows: rows, Err: err, At: time.Now()}
			s.mu.Lock()
			defer s.mu.Unlock()
			if err != nil {
				st.Failed++
				st.LastErr = err.Error()
				log.Printf("query failed: %v\n%s", err, truncate(j.sql, 200))
				if old != nil && old.Rows != nil {
					r.Columns, r.Rows, r.At, r.Stale = old.Columns, old.Rows, old.At, true
				}
			}
			s.results[j.sql] = r
		}(j, old)
	}
	wg.Wait()
	st.Took = time.Since(st.At)
	return st
}

func (s *Store) Get(cfg *Config, q Query) *Result {
	if q == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results[cfg.expand(q)]
}

// ---------------------------------------------------------------- values

func num(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0 // null
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------- demo

// demoResult makes up data in the shape a widget expects, keyed on the query
// text so a layout renders the same way every time. It lets layouts be designed
// without an API key.
func demoResult(job queryJob) *Result {
	h := fnv.New64a()
	h.Write([]byte(job.sql))
	r := rand.New(rand.NewSource(int64(h.Sum64())))
	base := []float64{40, 240, 1800}[r.Intn(3)]
	today := time.Now()
	days := func(cols int) [][]any {
		var rows [][]any
		for i := 13; i >= 0; i-- {
			row := []any{today.AddDate(0, 0, -i).Format("2006-01-02")}
			for c := 0; c < cols; c++ {
				k := math.Pow(8, float64(cols-1-c))
				row = append(row, json.Number(strconv.Itoa(int(base*k/7*(0.4+r.Float64())))))
			}
			rows = append(rows, row)
		}
		return rows
	}
	n := func(v float64) any { return json.Number(strconv.FormatFloat(v, 'f', 2, 64)) }
	res := &Result{At: time.Now()}
	switch job.shape {
	case "meta":
		res.Rows = [][]any{{"demo data"}}
	case "stat":
		v := base * (0.5 + r.Float64())
		res.Rows = [][]any{{n(v), n(v * (0.6 + 0.8*r.Float64()))}}
	case "series":
		res.Rows = days(1)
	case "chart:columns":
		for i := 23; i >= 0; i-- {
			res.Rows = append(res.Rows, []any{today.Add(-time.Duration(i) * time.Hour).Format("15"), n(float64(r.Intn(6)))})
		}
	case "chart":
		res.Columns = []string{"day", "impressions", "clicks"}
		res.Rows = days(2)
	case "bars":
		for i, name := range []string{"USD", "EUR", "GBP", "JPY"} {
			v := base / float64(i+1)
			res.Rows = append(res.Rows, []any{name, n(v), n(v * (0.5 + r.Float64()))})
		}
	case "bars:list":
		for i, name := range []string{"$direct", "www.google.com", "news.ycombinator.com", "t.co"} {
			res.Rows = append(res.Rows, []any{name, n(math.Round(base / float64(i*i+1)))})
		}
	case "segments":
		for _, name := range []string{"new", "renew", "life", "refund"} {
			res.Rows = append(res.Rows, []any{name, n(float64(r.Intn(6)))})
		}
	}
	return res
}

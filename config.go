package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is amber.json: data source credentials plus a screen made of rows of widgets.
type Config struct {
	Title          string  `json:"title"`
	Timezone       string  `json:"timezone,omitempty"`
	RefreshMinutes int     `json:"refresh_minutes,omitempty"`
	PostHog        PostHog `json:"posthog"`
	Web            Web     `json:"web"`
	Rows           []Row   `json:"rows"`

	location *time.Location
}

type PostHog struct {
	Host    string `json:"host"`
	Project string `json:"project"`
	APIKey  string `json:"api_key"`
}

type Web struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port,omitempty"`
	PIN     string `json:"pin,omitempty"`
}

// Row is a horizontal band of the screen. With a title it is drawn as a framed
// panel whose cells share the frame; without one every cell is its own card.
type Row struct {
	Height    int      `json:"height"`
	Title     string   `json:"title,omitempty"`
	Meta      string   `json:"meta,omitempty"`
	MetaQuery Query    `json:"meta_query,omitempty"`
	Cells     []Widget `json:"cells"`
}

// Widget is one visualization. Which fields matter depends on Type; see README.
type Widget struct {
	Type          string   `json:"type"`
	Title         string   `json:"title,omitempty"`
	Span          int      `json:"span,omitempty"`
	Style         string   `json:"style,omitempty"`
	Query         Query    `json:"query,omitempty"`
	Series        Query    `json:"series,omitempty"`
	Points        int      `json:"points,omitempty"`
	Format        string   `json:"format,omitempty"`
	LowerIsBetter bool     `json:"lower_is_better,omitempty"`
	LabelEvery    int      `json:"label_every,omitempty"`
	HighlightLast bool     `json:"highlight_last,omitempty"`
	CacheMinutes  int      `json:"cache_minutes,omitempty"`
	Items         []Widget `json:"items,omitempty"`
}

// Query is HogQL. In JSON it may be one string or an array of lines, which keeps
// long queries readable in the file.
type Query string

func (q *Query) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*q = Query(s)
		return nil
	}
	var lines []string
	if err := json.Unmarshal(b, &lines); err != nil {
		return errors.New("a query must be a string or an array of strings")
	}
	*q = Query(strings.Join(lines, "\n"))
	return nil
}

func (q Query) MarshalJSON() ([]byte, error) {
	if !strings.Contains(string(q), "\n") {
		return json.Marshal(string(q))
	}
	return json.Marshal(strings.Split(string(q), "\n"))
}

const maskedKey = "********"

var widgetTypes = map[string]bool{"stat": true, "chart": true, "bars": true, "segments": true, "stack": true}

func parseConfig(data []byte) (*Config, error) {
	c := &Config{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, err
	}
	return c, c.normalize()
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) normalize() error {
	if c.Title == "" {
		c.Title = "AMBER"
	}
	if c.RefreshMinutes <= 0 {
		c.RefreshMinutes = 10
	}
	if c.PostHog.Host == "" {
		c.PostHog.Host = "https://eu.posthog.com"
	}
	c.PostHog.Host = strings.TrimRight(c.PostHog.Host, "/")
	if c.Web.Port == 0 {
		c.Web.Port = 8080
	}
	c.location = time.UTC
	if c.Timezone != "" {
		loc, err := time.LoadLocation(c.Timezone)
		if err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
		c.location = loc
	}
	if len(c.Rows) == 0 {
		return errors.New("rows: the screen needs at least one row")
	}
	total := 0
	for i, r := range c.Rows {
		if r.Height <= 0 {
			return fmt.Errorf("rows[%d]: height must be positive", i)
		}
		total += r.Height
		if len(r.Cells) == 0 {
			return fmt.Errorf("rows[%d]: no cells", i)
		}
		for j := range r.Cells {
			if err := checkWidget(&c.Rows[i].Cells[j], fmt.Sprintf("rows[%d].cells[%d]", i, j)); err != nil {
				return err
			}
		}
	}
	if avail := screenH - headerH - footerH; total+rowGap*(len(c.Rows)-1) > avail {
		return fmt.Errorf("rows: heights plus gaps add up to %d px, the screen has %d", total+rowGap*(len(c.Rows)-1), avail)
	}
	return nil
}

func checkWidget(w *Widget, where string) error {
	if !widgetTypes[w.Type] {
		return fmt.Errorf("%s: unknown type %q (stat, chart, bars, segments, stack)", where, w.Type)
	}
	if w.Span <= 0 {
		w.Span = 1
	}
	if w.Type == "stack" {
		if len(w.Items) == 0 {
			return fmt.Errorf("%s: stack has no items", where)
		}
		for k := range w.Items {
			if err := checkWidget(&w.Items[k], fmt.Sprintf("%s.items[%d]", where, k)); err != nil {
				return err
			}
		}
		return nil
	}
	if w.Query == "" && w.Series == "" {
		return fmt.Errorf("%s: needs a query", where)
	}
	if w.Type != "stat" && w.Query == "" {
		return fmt.Errorf("%s: %s needs query", where, w.Type)
	}
	switch w.Format {
	case "", "count", "decimal1", "decimal2", "money", "percent":
	default:
		return fmt.Errorf("%s: unknown format %q (count, decimal1, decimal2, money, percent)", where, w.Format)
	}
	return nil
}

func (c *Config) Refresh() time.Duration { return time.Duration(c.RefreshMinutes) * time.Minute }

// Masked returns the config as JSON for the web UI, without the API key.
func (c *Config) Masked() ([]byte, error) {
	cp := *c
	if cp.PostHog.APIKey != "" {
		cp.PostHog.APIKey = maskedKey
	}
	return json.MarshalIndent(cp, "", "  ")
}

func saveConfig(path string, c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// expand fills placeholders a query may use.
func (c *Config) expand(q Query) string {
	tz := "UTC"
	if c.location != nil {
		tz = c.location.String()
	}
	return strings.ReplaceAll(string(q), "{timezone}", tz)
}

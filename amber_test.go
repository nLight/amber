package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExampleConfigsAreValid(t *testing.T) {
	for _, path := range []string{"examples/app-store.json", "examples/web-analytics.json"} {
		if _, err := loadConfig(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestQueryAcceptsStringOrLines(t *testing.T) {
	var w struct{ A, B Query }
	if err := json.Unmarshal([]byte(`{"A": "SELECT 1", "B": ["SELECT", "  1"]}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.A != "SELECT 1" || w.B != "SELECT\n  1" {
		t.Fatalf("got %q and %q", w.A, w.B)
	}
	out, _ := json.Marshal(w.B)
	if string(out) != `["SELECT","  1"]` {
		t.Fatalf("multi-line query should round-trip as lines, got %s", out)
	}
}

func TestConfigRejectsOverflowAndUnknownWidgets(t *testing.T) {
	base := `{"rows": [{"height": %s, "cells": [{"type": "%s", "query": "SELECT 1"}]}]}`
	cases := map[string]string{
		"too tall":     strings.Replace(strings.Replace(base, "%s", "900", 1), "%s", "stat", 1),
		"unknown type": strings.Replace(strings.Replace(base, "%s", "100", 1), "%s", "pie", 1),
	}
	for name, cfg := range cases {
		if _, err := parseConfig([]byte(cfg)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMaskedConfigHidesKeyAndKeepsItOnSave(t *testing.T) {
	c, err := loadConfig("examples/app-store.json")
	if err != nil {
		t.Fatal(err)
	}
	c.PostHog.APIKey = "phx_secret"
	masked, _ := c.Masked()
	if strings.Contains(string(masked), "phx_secret") {
		t.Fatal("masked config leaks the API key")
	}
	back, err := parseConfig(masked)
	if err != nil {
		t.Fatalf("masked config does not parse back: %v", err)
	}
	if back.PostHog.APIKey != maskedKey {
		t.Fatalf("expected the mask, got %q", back.PostHog.APIKey)
	}
}

func TestSeriesColumnsFillsMissingDays(t *testing.T) {
	rows := [][]any{
		{"2026-09-01", json.Number("3")},
		{"2026-09-04", json.Number("5")},
	}
	labels, cols := seriesColumns(rows, 5)
	want := []float64{0, 3, 0, 0, 5}
	if len(labels) != 5 || labels[4] != "2026-09-04" || labels[0] != "2026-08-31" {
		t.Fatalf("labels %v", labels)
	}
	for i, v := range want {
		if cols[0][i] != v {
			t.Fatalf("values %v, want %v", cols[0], want)
		}
	}
}

func TestStatWithoutQueryComparesSeriesHalves(t *testing.T) {
	cfg := &Config{}
	store := newStore()
	w := Widget{Type: "stat", Series: "S", Points: 4}
	store.results["S"] = &Result{Rows: [][]any{
		{"2026-09-01", json.Number("1")}, {"2026-09-02", json.Number("2")},
		{"2026-09-03", json.Number("3")}, {"2026-09-04", json.Number("4")},
	}}
	p, hasPrev, s, _ := statValues(cfg, store, w)
	if !hasPrev || p.Cur != 7 || p.Prev != 3 || len(s) != 4 {
		t.Fatalf("got %+v prev=%v series=%v", p, hasPrev, s)
	}
}

func TestDeltaFormats(t *testing.T) {
	cases := []struct {
		p      pair
		format string
		lower  bool
		label  string
		good   bool
	}{
		{pair{4, 2}, "count", false, "+2", true},
		{pair{240, 425}, "count", false, "-44%", false},
		{pair{7.9, 9.1}, "decimal1", true, "-1.2", true},
		{pair{32.7, 10.9}, "money", false, "+200%", true},
	}
	for _, tc := range cases {
		label, good, _ := delta(tc.p, tc.format, tc.lower)
		if label != tc.label || good != tc.good {
			t.Errorf("delta(%v, %s) = %s good=%v, want %s good=%v", tc.p, tc.format, label, good, tc.label, tc.good)
		}
	}
}

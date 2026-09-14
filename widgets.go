package main

import (
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"
	"time"
)

type pair struct{ Cur, Prev float64 }

// Series holds one value per point, oldest first.
type Series []float64

// ---------------------------------------------------------------- formats

func decimalsFor(format string) int {
	switch format {
	case "decimal1":
		return 1
	case "decimal2", "money", "percent":
		return 2
	}
	return 0
}

func fmtValue(v float64, format string) string {
	switch format {
	case "decimal1", "decimal2", "money":
		return strconv.FormatFloat(v, 'f', decimalsFor(format), 64)
	case "percent":
		return strconv.FormatFloat(v, 'f', 2, 64) + "%"
	}
	return fmtCount(v)
}

// delta describes a change: the label, whether it is good news, and whether
// anything moved. Counts get an absolute difference while small and a percentage
// once big, where a raw difference says little; money always gets a percentage.
func delta(p pair, format string, lowerIsBetter bool) (label string, good, changed bool) {
	decimals := decimalsFor(format)
	diff := p.Cur - p.Prev
	if math.Abs(diff) < math.Pow10(-decimals)/2 {
		return "0", true, false
	}
	good = (diff > 0) != lowerIsBetter
	sign := "+"
	if diff < 0 {
		sign = "-"
	}
	if (format == "count" || format == "" || format == "money") && p.Prev >= 50 || format == "money" && p.Prev > 0 {
		return fmt.Sprintf("%s%.0f%%", sign, math.Abs(100*diff/p.Prev)), good, true
	}
	return sign + strconv.FormatFloat(math.Abs(diff), 'f', decimals, 64), good, true
}

// ---------------------------------------------------------------- screen

type screenState struct {
	battery string
	webURL  string
	notice  string // shown instead of the status line, e.g. while setup is unfinished
	power   string // how the device refreshes, for the header
	next    time.Time
	syncing bool
}

// renderScreen draws the whole frame from the config and whatever the store holds.
func renderScreen(cfg *Config, f *faces, store *Store, stats Stats, st screenState) *image.Gray {
	c := &canvas{Gray: image.NewGray(image.Rect(0, 0, screenW, screenH)), f: f}
	c.fill(c.Rect, paper)
	// The big time in the header is when the data was fetched: between updates the
	// Kindle may be asleep, so a wall clock would be wrong most of the time.
	now := time.Now().In(cfg.location)
	if !stats.At.IsZero() {
		now = stats.At.In(cfg.location)
	}
	renderHeader(c, cfg, now, st)

	y := headerH
	for _, row := range cfg.Rows {
		r := image.Rect(margin, y, screenW-margin, y+row.Height)
		if row.Title != "" {
			meta := row.Meta
			if res := store.Get(cfg, row.MetaQuery); res != nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
				meta = str(res.Rows[0][0])
			}
			in := c.panel(r, upper(row.Title), meta)
			cells := splitSpans(in, row.Cells, 24)
			for i, w := range row.Cells {
				if i > 0 {
					c.vdotted(cells[i].Min.X-12, in.Min.Y, in.Max.Y, ink3, 3)
				}
				c.widget(cfg, store, w, cells[i], false)
			}
		} else {
			cells := splitSpans(r, row.Cells, 8)
			for i, w := range row.Cells {
				c.widget(cfg, store, w, cells[i], true)
			}
		}
		y += row.Height + rowGap
	}

	renderFooter(c, cfg, now, stats, st)
	c.quantize()
	return c.Gray
}

// splitSpans divides r horizontally in proportion to each widget's span.
func splitSpans(r image.Rectangle, ws []Widget, gap int) []image.Rectangle {
	total := 0
	for _, w := range ws {
		total += w.Span
	}
	width := r.Dx() - gap*(len(ws)-1)
	out := make([]image.Rectangle, len(ws))
	x := r.Min.X
	for i, w := range ws {
		cw := width * w.Span / total
		if i == len(ws)-1 {
			cw = r.Max.X - x
		}
		out[i] = image.Rect(x, r.Min.Y, x+cw, r.Max.Y)
		x += cw + gap
	}
	return out
}

func renderHeader(c *canvas, cfg *Config, now time.Time, st screenState) {
	c.fill(image.Rect(0, 0, screenW, 60), black)
	x := c.text(c.f.title, margin+2, 34, cfg.Title, paper)
	c.text(c.f.small, x+10, 34, "// amber", ink3)
	tx := c.textRight(c.f.title, screenW-margin, 34, now.Format("15:04"), paper)
	c.textRight(c.f.tiny, tx-8, 33, "UPDATED", ink3)
	c.text(c.f.tiny, margin+2, 52, upper(now.Format("Mon 02 Jan"))+"  ·  "+st.power, ink4)

	right := screenW - margin
	if st.battery != "" {
		lvl, _ := strconv.Atoi(st.battery)
		body := image.Rect(right-26, 42, right-3, 54)
		c.stroke(body, ink5, 1)
		c.fill(image.Rect(right-3, 45, right, 51), ink5)
		c.fill(image.Rect(body.Min.X+2, body.Min.Y+2, body.Min.X+2+(body.Dx()-4)*lvl/100, body.Max.Y-2), paper)
		right = c.textRight(c.f.tiny, right-32, 52, st.battery+"%", ink4) - 14
	}
	right = c.textRight(c.f.tiny, right, 52, "wifi", ink4) - 8
	for i := 0; i < 4; i++ {
		c.fill(image.Rect(right-(4-i)*5, 52-3-i*2, right-(4-i)*5+3, 52), ink4)
	}
	for x := 0; x < screenW; x += 4 {
		c.fill(image.Rect(x, 60, x+2, 63), black)
	}
}

func renderFooter(c *canvas, cfg *Config, now time.Time, stats Stats, st screenState) {
	y := screenH - 22
	c.hline(0, screenW, y, black)
	switch {
	case st.syncing:
		c.fill(image.Rect(0, y, screenW, screenH), black)
		c.text(c.f.tiny, margin, y+16, "> syncing with posthog ...", paper)
		return
	case st.notice != "":
		c.fill(image.Rect(0, y, screenW, screenH), black)
		c.text(c.f.tiny, margin, y+16, truncate("> "+st.notice, 82), paper)
		return
	case stats.Failed > 0:
		c.fill(image.Rect(0, y, screenW, screenH), black)
		msg := fmt.Sprintf("!! %d/%d queries failed: %s", stats.Failed, stats.Ran, stats.LastErr)
		c.text(c.f.tiny, margin, y+16, truncate(msg, 82), paper)
		return
	}
	left := fmt.Sprintf("%dq", stats.Ran)
	if stats.Cached > 0 {
		left += fmt.Sprintf("+%d cached", stats.Cached)
	}
	left += fmt.Sprintf("  %.1fs  next %s", stats.Took.Seconds(), st.next.In(cfg.location).Format("15:04"))
	c.text(c.f.tiny, margin, y+16, left, ink1)
	if st.webURL != "" {
		c.textRight(c.f.tiny, screenW-margin, y+16, st.webURL, black)
	}
}

// ---------------------------------------------------------------- widgets

// widget draws w into r. card is true when the widget stands alone in an
// untitled row and so draws its own frame.
func (c *canvas) widget(cfg *Config, store *Store, w Widget, r image.Rectangle, card bool) {
	if w.Type == "stack" {
		total := 0
		for _, it := range w.Items {
			total += it.Span
		}
		y := r.Min.Y
		for i, it := range w.Items {
			h := r.Dy() * it.Span / total
			if i == len(w.Items)-1 {
				h = r.Max.Y - y
			}
			if i > 0 {
				c.dotted(r.Min.X, r.Max.X, y-4, ink4, 2)
			}
			c.widget(cfg, store, it, image.Rect(r.Min.X, y, r.Max.X, y+h-8), false)
			y += h
		}
		return
	}

	if w.Type == "stat" && (card && w.Style == "" || w.Style == "tile") {
		c.statTile(cfg, store, w, r)
		return
	}
	if card {
		r = c.panel(r, upper(w.Title), "")
	} else if w.Title != "" && w.Type != "stat" {
		c.label(r.Min.X, r.Min.Y+10, upper(w.Title))
		r.Min.Y += 18
	}

	switch w.Type {
	case "stat":
		if w.Style == "readout" {
			c.statReadout(cfg, store, w, r)
		} else {
			c.statInline(cfg, store, w, r)
		}
	case "chart":
		if w.Style == "columns" {
			c.chartColumns(cfg, store, w, r)
		} else {
			c.chartAreaLine(cfg, store, w, r)
		}
	case "bars":
		c.bars(cfg, store, w, r)
	case "segments":
		c.segments(cfg, store, w, r)
	}
}

// problem draws an error or loading state inside r and reports whether it did.
func (c *canvas) problem(r image.Rectangle, results ...*Result) bool {
	for _, res := range results {
		if res == nil {
			continue
		}
		if res.Err != nil && !res.Stale {
			box := image.Rect(r.Min.X, r.Min.Y, r.Max.X, min(r.Max.Y, r.Min.Y+44))
			c.hatch(box, ink4, 4)
			c.fill(image.Rect(box.Min.X, box.Min.Y, box.Min.X+34, box.Min.Y+18), black)
			c.text(c.f.smallB, box.Min.X+5, box.Min.Y+14, "ERR", paper)
			msg := res.Err.Error()
			maxc := max(8, (box.Dx()-44)/7)
			c.fill(image.Rect(box.Min.X+38, box.Min.Y+2, box.Min.X+40+min(len(msg), maxc)*7, box.Min.Y+16), paper)
			c.text(c.f.tiny, box.Min.X+40, box.Min.Y+13, truncate(msg, maxc), black)
			if len(msg) > maxc {
				rest := truncate(msg[maxc:], (box.Dx()-4)/7)
				c.fill(image.Rect(box.Min.X+2, box.Min.Y+22, box.Min.X+4+len(rest)*7, box.Min.Y+36), paper)
				c.text(c.f.tiny, box.Min.X+4, box.Min.Y+33, rest, black)
			}
			return true
		}
	}
	for _, res := range results {
		if res == nil {
			c.text(c.f.tiny, r.Min.X, r.Min.Y+14, "waiting for data ...", ink3)
			return true
		}
	}
	return false
}

// statValues resolves a stat widget: value and previous from the query's first row,
// the sparkline from series. Without a query, value and previous are the sums of
// the second and first halves of the series.
func statValues(cfg *Config, store *Store, w Widget) (p pair, hasPrev bool, s Series, results []*Result) {
	var q, sr *Result
	if w.Query != "" {
		q = store.Get(cfg, w.Query)
		results = append(results, q)
	}
	if w.Series != "" {
		sr = store.Get(cfg, w.Series)
		results = append(results, sr)
		if sr != nil {
			_, cols := seriesColumns(sr.Rows, w.Points)
			if len(cols) > 0 {
				s = cols[0]
			}
		}
	}
	if q != nil && len(q.Rows) > 0 {
		row := q.Rows[0]
		if len(row) > 0 {
			p.Cur = num(row[0])
		}
		if len(row) > 1 && row[1] != nil {
			p.Prev, hasPrev = num(row[1]), true
		}
		if len(row) > 2 && s == nil {
			if arr, ok := row[2].([]any); ok {
				for _, v := range arr {
					s = append(s, num(v))
				}
			}
		}
	} else if w.Query == "" && len(s) > 0 {
		half := len(s) / 2
		for i, v := range s {
			if i < half {
				p.Prev += v
			} else {
				p.Cur += v
			}
		}
		hasPrev = true
	}
	return
}

// statTile is a KPI card: big number, change badge and a bar sparkline where the
// first half of the series (the previous period) is hatched.
func (c *canvas) statTile(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	c.stroke(r, black, 1)
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+4, r.Min.Y+30), black)
	c.text(c.f.smallB, r.Min.X+14, r.Min.Y+20, upper(w.Title), black)

	p, hasPrev, s, results := statValues(cfg, store, w)
	inner := image.Rect(r.Min.X+12, r.Min.Y+30, r.Max.X-10, r.Max.Y-10)
	if c.problem(inner, results...) {
		return
	}
	if len(results) > 0 && results[0] != nil && results[0].Stale {
		c.textRight(c.f.tiny, r.Max.X-10, r.Min.Y+19, "stale", ink2)
	}
	c.text(c.f.big, r.Min.X+12, r.Min.Y+70, fmtValue(p.Cur, w.Format), black)
	if hasPrev {
		c.badge(r.Max.X-10, r.Min.Y+34, p, w.Format, w.LowerIsBetter)
		c.textRight(c.f.tiny, r.Max.X-10, r.Min.Y+72, "prev "+fmtValue(p.Prev, w.Format), ink2)
	}
	c.weekBars(image.Rect(r.Min.X+12, r.Min.Y+84, r.Max.X-10, r.Max.Y-10), s)
}

// statInline is a compact stat for panels: label, value with a change arrow and a
// step line of the series to the right.
func (c *canvas) statInline(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	c.label(r.Min.X, r.Min.Y+10, upper(w.Title))
	p, hasPrev, s, results := statValues(cfg, store, w)
	if c.problem(image.Rect(r.Min.X, r.Min.Y+16, r.Max.X, r.Max.Y), results...) {
		return
	}
	x := c.text(c.f.mid, r.Min.X, r.Min.Y+34, fmtValue(p.Cur, w.Format), black)
	if lbl, good, changed := delta(p, w.Format, w.LowerIsBetter); hasPrev && changed {
		v := black
		if !good {
			v = ink2
		}
		c.triangle(x+9, r.Min.Y+27, 9, p.Cur > p.Prev, v)
		x = c.text(c.f.smallB, x+16, r.Min.Y+33, lbl, v)
	}
	if len(s) > 1 {
		c.stepLine(image.Rect(max(x+12, r.Min.X+r.Dx()*38/100), r.Min.Y+12, r.Max.X, r.Min.Y+34), s)
	}
}

// statReadout is a label and value with the change badge on the right.
func (c *canvas) statReadout(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	c.label(r.Min.X, r.Min.Y+10, upper(w.Title))
	p, hasPrev, _, results := statValues(cfg, store, w)
	if c.problem(image.Rect(r.Min.X, r.Min.Y+14, r.Max.X, r.Max.Y), results...) {
		return
	}
	c.text(c.f.mid, r.Min.X, r.Min.Y+32, fmtValue(p.Cur, w.Format), black)
	if hasPrev {
		c.badge(r.Max.X, r.Min.Y+14, p, w.Format, w.LowerIsBetter)
	}
}

// seriesColumns turns (label, value...) rows into labels and one series per value
// column. Date labels get missing days filled with zeros, ending at the last date
// present, so queries do not have to pad their own gaps.
func seriesColumns(rows [][]any, points int) (labels []string, cols []Series) {
	if len(rows) == 0 || len(rows[0]) < 2 {
		return nil, nil
	}
	n := len(rows[0]) - 1
	byDay := map[string][]float64{}
	var last time.Time
	dated := true
	for _, r := range rows {
		t, err := time.Parse("2006-01-02", truncate(str(r[0]), 10))
		if err != nil {
			dated = false
			break
		}
		key := t.Format("2006-01-02")
		if byDay[key] == nil {
			byDay[key] = make([]float64, n)
		}
		for i := 0; i < n && i+1 < len(r); i++ {
			byDay[key][i] += num(r[i+1])
		}
		if t.After(last) {
			last = t
		}
	}
	cols = make([]Series, n)
	if !dated {
		for _, r := range rows {
			labels = append(labels, str(r[0]))
			for i := 0; i < n; i++ {
				cols[i] = append(cols[i], num(r[i+1]))
			}
		}
		if points > 0 && len(labels) > points {
			labels = labels[len(labels)-points:]
			for i := range cols {
				cols[i] = cols[i][len(cols[i])-points:]
			}
		}
		return labels, cols
	}
	if points <= 0 {
		points = 14
	}
	for d := points - 1; d >= 0; d-- {
		day := last.AddDate(0, 0, -d).Format("2006-01-02")
		labels = append(labels, day)
		vals := byDay[day]
		for i := 0; i < n; i++ {
			v := 0.0
			if vals != nil {
				v = vals[i]
			}
			cols[i] = append(cols[i], v)
		}
	}
	return labels, cols
}

// chartColumns draws one series as columns with a y-scale, thinned x labels and
// the latest point marked.
func (c *canvas) chartColumns(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	res := store.Get(cfg, w.Query)
	if c.problem(r, res) {
		return
	}
	labels, cols := seriesColumns(res.Rows, w.Points)
	if len(cols) == 0 {
		c.text(c.f.tiny, r.Min.X, r.Min.Y+14, "no rows", ink3)
		return
	}
	s := cols[0]
	chart := image.Rect(r.Min.X+18, r.Min.Y+4, r.Max.X, r.Max.Y-18)
	m := math.Max(1, maxOf(s))
	c.textRight(c.f.tiny, chart.Min.X-4, chart.Min.Y+8, fmtCount(m), ink3)
	c.textRight(c.f.tiny, chart.Min.X-4, chart.Max.Y, "0", ink3)
	c.dotted(chart.Min.X, chart.Max.X, chart.Min.Y, ink4, 3)
	c.dotted(chart.Min.X, chart.Max.X, chart.Min.Y+chart.Dy()/2, ink4, 3)
	c.hline(chart.Min.X, chart.Max.X, chart.Max.Y, black)
	every := w.LabelEvery
	if every <= 0 {
		every = max(1, len(s)/4)
	}
	slot := float64(chart.Dx()) / float64(len(s))
	for i, v := range s {
		x := chart.Min.X + int(float64(i)*slot)
		h := int(math.Round(v / m * float64(chart.Dy())))
		bar := image.Rect(x+1, chart.Max.Y-h, x+int(math.Max(2, slot-1)), chart.Max.Y)
		last := i == len(s)-1
		if last && w.HighlightLast {
			c.fill(bar, black)
			c.triangle(x+int(slot)/2, chart.Max.Y-h-6, 7, false, black)
		} else {
			c.fill(bar, ink2)
		}
		if (len(s)-1-i)%every == 0 {
			label := labels[i]
			if t, err := time.Parse("2006-01-02", label); err == nil {
				label = t.Format("02")
			}
			c.vline(x+int(slot)/2, chart.Max.Y, chart.Max.Y+3, black)
			c.text(c.f.tiny, x+int(slot)/2-textWidth(c.f.tiny, label)/2, chart.Max.Y+15, label, ink2)
		}
	}
}

// chartAreaLine draws the first series as a gray area and the second as a black
// line with markers, each on its own scale, with a legend comparing halves.
func (c *canvas) chartAreaLine(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	res := store.Get(cfg, w.Query)
	if c.problem(r, res) {
		return
	}
	_, cols := seriesColumns(res.Rows, w.Points)
	if len(cols) == 0 || len(cols[0]) < 2 {
		c.text(c.f.tiny, r.Min.X, r.Min.Y+14, "no rows", ink3)
		return
	}
	names := res.Columns
	legend := func(x, i int) int {
		s := cols[i]
		var p pair
		for j, v := range s {
			if j < len(s)/2 {
				p.Prev += v
			} else {
				p.Cur += v
			}
		}
		name := fmt.Sprintf("s%d", i+1)
		if i+1 < len(names) {
			name = names[i+1]
		}
		lbl, _, _ := delta(p, w.Format, false)
		if i == 0 {
			c.fill(image.Rect(x, r.Min.Y+2, x+10, r.Min.Y+12), ink4)
		} else {
			c.fill(image.Rect(x, r.Min.Y+5, x+10, r.Min.Y+8), black)
		}
		return c.text(c.f.tiny, x+14, r.Min.Y+12, fmt.Sprintf("%s %s %s", name, fmtValue(p.Cur, w.Format), lbl), ink1) + 12
	}
	x := legend(r.Min.X, 0)
	if len(cols) > 1 {
		legend(x, 1)
	}

	chart := image.Rect(r.Min.X, r.Min.Y+20, r.Max.X, r.Max.Y)
	c.hline(chart.Min.X, chart.Max.X, chart.Max.Y, black)
	area := cols[0]
	n := len(area)
	px := func(i int) float64 { return float64(chart.Min.X) + float64(i)*float64(chart.Dx()-1)/float64(n-1) }
	if ma := maxOf(area); ma > 0 {
		for xx := chart.Min.X; xx < chart.Max.X; xx++ {
			t := float64(xx-chart.Min.X) / float64(chart.Dx()-1) * float64(n-1)
			i := min(int(t), n-2)
			v := area[i] + (area[i+1]-area[i])*(t-float64(i))
			h := int(v / ma * float64(chart.Dy()))
			c.fill(image.Rect(xx, chart.Max.Y-h, xx+1, chart.Max.Y), ink5)
			c.set(xx, chart.Max.Y-h, ink3)
		}
	}
	c.vdotted(int((px(n/2-1)+px(n/2))/2), chart.Min.Y, chart.Max.Y, ink2, 2)
	if len(cols) > 1 {
		line := cols[1]
		if ml := maxOf(line); ml > 0 {
			py := func(v float64) float64 { return float64(chart.Max.Y-3) - v/ml*float64(chart.Dy()-6) }
			for i := 1; i < n && i < len(line); i++ {
				c.line(px(i-1), py(line[i-1]), px(i), py(line[i]), black, 2)
			}
			for i := 0; i < n && i < len(line); i++ {
				cx, cy := int(px(i)), int(py(line[i]))
				c.fill(image.Rect(cx-2, cy-2, cx+3, cy+3), black)
				c.fill(image.Rect(cx-1, cy-1, cx+2, cy+2), paper)
			}
		}
	}
}

// bars is a ranked list. Rows are (label, value) or (label, value, previous); with
// a previous column each row gets a big value, a change and a hatched previous bar.
func (c *canvas) bars(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	res := store.Get(cfg, w.Query)
	if c.problem(r, res) {
		return
	}
	if len(res.Rows) == 0 {
		c.text(c.f.small, r.Min.X, r.Min.Y+20, "nothing yet", ink3)
		return
	}
	compare := len(res.Rows[0]) > 2
	top := 0.0
	for _, row := range res.Rows {
		for i := 1; i < len(row) && i < 3; i++ {
			top = math.Max(top, num(row[i]))
		}
	}
	if compare {
		for i, row := range res.Rows {
			ry := r.Min.Y + i*42
			if ry+36 > r.Max.Y {
				break
			}
			p := pair{num(row[1]), num(row[2])}
			m := math.Max(p.Cur, p.Prev)
			x := c.text(c.f.smallB, r.Min.X, ry+16, truncate(str(row[0]), 6), ink2)
			c.text(c.f.mid, max(x+8, r.Min.X+36), ry+17, fmtValue(p.Cur, w.Format), black)
			label := "new"
			if p.Prev != 0 {
				label, _, _ = delta(p, "money", w.LowerIsBetter)
			}
			c.textRight(c.f.smallB, r.Max.X, ry+16, label, black)
			if m <= 0 {
				continue
			}
			c.fill(image.Rect(r.Min.X, ry+23, r.Min.X+int(p.Cur/m*float64(r.Dx())), ry+30), black)
			c.hatch(image.Rect(r.Min.X, ry+32, r.Min.X+int(p.Prev/m*float64(r.Dx())), ry+36), ink1, 2)
			c.hline(r.Min.X, r.Max.X, ry+36, ink4)
		}
		return
	}
	for i, row := range res.Rows {
		ry := r.Min.Y + 12 + i*21
		if ry+6 > r.Max.Y {
			break
		}
		name := strings.TrimPrefix(str(row[0]), "www.")
		if name == "$direct" || name == "" {
			name = "(direct)"
		}
		v := num(row[1])
		c.text(c.f.small, r.Min.X, ry, truncate(name, max(4, r.Dx()/9-5)), black)
		c.textRight(c.f.smallB, r.Max.X, ry, fmtValue(v, w.Format), black)
		if top > 0 {
			c.dotted(r.Min.X, r.Max.X, ry+4, ink4, 2)
			c.fill(image.Rect(r.Min.X, ry+3, r.Min.X+int(v/top*float64(r.Dx())), ry+6), black)
		}
	}
}

// segments is a proportional bar with a legend. Rows are (label, value), or a
// single row whose column names are the labels.
func (c *canvas) segments(cfg *Config, store *Store, w Widget, r image.Rectangle) {
	res := store.Get(cfg, w.Query)
	if c.problem(r, res) {
		return
	}
	type seg struct {
		name string
		v    float64
	}
	var segs []seg
	if len(res.Rows) == 1 && len(res.Rows[0]) > 2 && len(res.Columns) == len(res.Rows[0]) {
		for i, v := range res.Rows[0] {
			segs = append(segs, seg{res.Columns[i], num(v)})
		}
	} else {
		for _, row := range res.Rows {
			if len(row) >= 2 {
				segs = append(segs, seg{str(row[0]), num(row[1])})
			}
		}
	}
	paints := []func(image.Rectangle){
		func(r image.Rectangle) { c.fill(r, black) },
		func(r image.Rectangle) { c.fill(r, ink2); c.checker(r, black) },
		func(r image.Rectangle) { c.fill(r, ink4) },
		func(r image.Rectangle) { c.hatch(r, black, 3) },
		func(r image.Rectangle) { c.checker(r, ink3) },
		func(r image.Rectangle) { c.fill(r, ink3) },
	}
	bar := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+20)
	c.stroke(bar, black, 1)
	total := 0.0
	for _, s := range segs {
		total += math.Max(0, s.v)
	}
	if total > 0 {
		x := bar.Min.X + 1
		for i, s := range segs {
			sw := int(math.Round(math.Max(0, s.v) / total * float64(bar.Dx()-2)))
			if i == len(segs)-1 {
				sw = bar.Max.X - 1 - x
			}
			if sw > 0 {
				paints[i%len(paints)](image.Rect(x, bar.Min.Y+1, x+sw, bar.Max.Y-1))
				c.vline(x+sw, bar.Min.Y, bar.Max.Y, paper)
			}
			x += sw
		}
	} else {
		c.text(c.f.tiny, bar.Min.X+8, bar.Min.Y+15, "all zero", ink3)
	}
	for i, s := range segs {
		lx := r.Min.X + (i%2)*(r.Dx()/2)
		ly := bar.Max.Y + 7 + (i/2)*16
		if ly+11 > r.Max.Y {
			break
		}
		sw := image.Rect(lx, ly, lx+11, ly+11)
		c.stroke(sw, black, 1)
		paints[i%len(paints)](sw.Inset(1))
		c.text(c.f.small, lx+17, ly+11, fmt.Sprintf("%s %s", s.name, fmtValue(s.v, w.Format)), black)
	}
}

// ---------------------------------------------------------------- small drawings

// badge draws a change pill ending at right: solid black for good news, outlined
// for bad news, gray when nothing moved. Returns its left edge.
func (c *canvas) badge(right, top int, p pair, format string, lowerIsBetter bool) int {
	label, good, changed := delta(p, format, lowerIsBetter)
	f := c.f.smallB
	w := textWidth(f, label) + 30
	r := image.Rect(right-w, top, right, top+22)
	up := p.Cur > p.Prev
	switch {
	case !changed:
		c.stroke(r, ink3, 1)
		c.text(f, r.Min.X+22, top+16, label, ink2)
		c.hline(r.Min.X+7, r.Min.X+16, top+10, ink2)
		c.hline(r.Min.X+7, r.Min.X+16, top+13, ink2)
	case good:
		c.fill(r, black)
		c.triangle(r.Min.X+12, top+11, 9, up, paper)
		c.text(f, r.Min.X+22, top+16, label, paper)
	default:
		c.stroke(r, black, 2)
		c.triangle(r.Min.X+12, top+11, 9, up, black)
		c.text(f, r.Min.X+22, top+16, label, black)
	}
	return r.Min.X
}

// panel draws a frame with an inverted title tab and returns the content area.
func (c *canvas) panel(r image.Rectangle, title, meta string) image.Rectangle {
	c.stroke(r, black, 1)
	if title != "" {
		tw := textWidth(c.f.smallB, title) + 20
		c.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+tw, r.Min.Y+22), black)
		c.text(c.f.smallB, r.Min.X+10, r.Min.Y+16, title, paper)
	}
	c.fill(image.Rect(r.Max.X-8, r.Max.Y-3, r.Max.X, r.Max.Y), black)
	c.fill(image.Rect(r.Max.X-3, r.Max.Y-8, r.Max.X, r.Max.Y), black)
	if meta != "" {
		c.textRight(c.f.tiny, r.Max.X-10, r.Min.Y+16, meta, ink2)
	}
	return image.Rect(r.Min.X+10, r.Min.Y+30, r.Max.X-10, r.Max.Y-8)
}

func (c *canvas) label(x, y int, s string) { c.text(c.f.tiny, x, y, s, ink2) }

// weekBars draws bars for a series: the first half (previous period) hatched, the
// second solid.
func (c *canvas) weekBars(r image.Rectangle, s Series) {
	c.hline(r.Min.X, r.Max.X, r.Max.Y, ink3)
	if len(s) == 0 {
		return
	}
	m := maxOf(s)
	slot := float64(r.Dx()) / float64(len(s))
	bw := int(slot * 0.66)
	for i, v := range s {
		x := r.Min.X + int(float64(i)*slot)
		h := 0
		if m > 0 {
			h = int(math.Round(v / m * float64(r.Dy())))
		}
		if v > 0 && h < 2 {
			h = 2
		}
		bar := image.Rect(x, r.Max.Y-h, x+bw, r.Max.Y)
		if i < len(s)/2 {
			c.stroke(bar, ink2, 1)
			c.hatch(bar, ink2, 3)
		} else {
			c.fill(bar, black)
		}
		if v == 0 {
			c.fill(image.Rect(x+bw/2-1, r.Max.Y-2, x+bw/2+1, r.Max.Y), ink3)
		}
	}
	mid := r.Min.X + int(slot*float64(len(s)/2)) - int(slot-float64(bw))/2 - 1
	c.vdotted(mid, r.Min.Y-4, r.Max.Y, ink2, 2)
}

func (c *canvas) stepLine(r image.Rectangle, s Series) {
	c.dotted(r.Min.X, r.Max.X, r.Max.Y, ink3, 2)
	if len(s) < 2 {
		return
	}
	lo, hi := s[0], s[0]
	for _, v := range s {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi == lo {
		lo, hi = lo-1, hi+1
	}
	y := func(v float64) float64 { return float64(r.Max.Y-2) - (v-lo)/(hi-lo)*float64(r.Dy()-4) }
	step := float64(r.Dx()) / float64(len(s))
	for i, v := range s {
		x0 := float64(r.Min.X) + float64(i)*step
		c.line(x0, y(v), x0+step, y(v), black, 2)
		if i > 0 && s[i-1] != v {
			c.line(x0, y(s[i-1]), x0, y(v), black, 2)
		}
	}
	c.textRight(c.f.tiny, r.Max.X, r.Min.Y-2, fmt.Sprintf("%s-%s", fmtCount(lo), fmtCount(hi)), ink3)
}

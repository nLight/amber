package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

//go:embed web/index.html
var webFiles embed.FS

// The web UI is reachable by anyone on the same Wi-Fi, so it sits behind the PIN
// shown on the Kindle's screen. Wrong guesses are slowed down to make four digits
// good enough on a home network.
type sessions struct {
	mu       sync.Mutex
	tokens   map[string]time.Time
	failures int
	lockout  time.Time
}

func (s *sessions) valid(r *http.Request) bool {
	c, err := r.Cookie("amber")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[c.Value]
	return ok && time.Now().Before(exp)
}

func (s *sessions) login(w http.ResponseWriter, pin, want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.lockout) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(want)) != 1 {
		s.failures++
		if s.failures >= 5 {
			s.lockout, s.failures = time.Now().Add(time.Minute), 0
		}
		return false
	}
	s.failures = 0
	b := make([]byte, 24)
	rand.Read(b)
	token := hex.EncodeToString(b)
	s.tokens[token] = time.Now().Add(30 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "amber", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// parseSubmitted reads a config posted by the UI. The API key comes back masked,
// so the masked value means "keep the current key".
func (a *App) parseSubmitted(r *http.Request) (*Config, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	c, err := parseConfig(body)
	if err != nil {
		return nil, err
	}
	if c.PostHog.APIKey == maskedKey {
		c.PostHog.APIKey = a.config().PostHog.APIKey
	}
	return c, nil
}

func (a *App) serveWeb() {
	s := &sessions{tokens: map[string]time.Time{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page, _ := webFiles.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})

	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			PIN string `json:"pin"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in)
		if !s.login(w, in.PIN, a.pin) {
			time.Sleep(time.Second)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong PIN — it is shown at the bottom of the Kindle screen"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.valid(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/api/state", auth(func(w http.ResponseWriter, r *http.Request) {
		c := a.config()
		masked, _ := c.Masked()
		a.mu.Lock()
		stats := a.stats
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"config": string(masked),
			"demo":   a.demo,
			"stats": map[string]any{
				"at": stats.At, "took_ms": stats.Took.Milliseconds(), "ran": stats.Ran,
				"cached": stats.Cached, "failed": stats.Failed, "last_error": stats.LastErr,
			},
			"battery": battery(),
		})
	}))

	mux.HandleFunc("/api/screen.png", auth(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		frame := a.frame
		a.mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(frame)
	}))

	mux.HandleFunc("/api/preview", auth(func(w http.ResponseWriter, r *http.Request) {
		c, err := a.parseSubmitted(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		stats := a.store.Fetch(c, a.source(c), false, 2*time.Minute)
		img := renderScreen(c, a.faces, a.store, stats, screenState{battery: battery(), webURL: a.webURL(c), notice: a.notice(c)})
		var buf bytes.Buffer
		png.Encode(&buf, img)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Amber-Failed", fmt.Sprint(stats.Failed))
		w.Header().Set("X-Amber-Error", stats.LastErr)
		w.Write(buf.Bytes())
	}))

	mux.HandleFunc("/api/config", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT only", http.StatusMethodNotAllowed)
			return
		}
		c, err := a.parseSubmitted(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := saveConfig(a.path, c); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		a.setConfig(c)
		a.requestSync(true)
		log.Printf("config saved from the web UI")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("/api/sync", auth(func(w http.ResponseWriter, r *http.Request) {
		a.requestSync(true)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	addr := fmt.Sprintf(":%d", a.config().Web.Port)
	log.Printf("web UI on http://%s%s, pin %s", localIP(), addr, a.pin)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("web: %v", err)
	}
}

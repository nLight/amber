// Amber turns a jailbroken Kindle into a PostHog dashboard. It runs on the device,
// queries PostHog over Wi-Fi, renders a grayscale frame from widgets described in
// amber.json and paints it with FBInk. A small web UI edits that file.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // the Kindle has no usable zoneinfo for Go
)

type App struct {
	path  string
	demo  bool
	fbink string
	faces *faces
	store *Store
	http  *http.Client
	pin   string
	syncs chan bool // a request to refresh; true forces cached queries too

	mu    sync.Mutex
	cfg   *Config
	stats Stats
	frame []byte // the last painted frame as PNG, for the web UI
}

func (a *App) config() *Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) setConfig(c *Config) {
	a.mu.Lock()
	a.cfg = c
	a.mu.Unlock()
}

// source returns nil (demo data) in demo mode and until an API key is set, so a
// fresh install shows a full screen that says where to finish setup.
func (a *App) source(c *Config) Source {
	if a.demo || c.PostHog.APIKey == "" || c.PostHog.Project == "" {
		return nil
	}
	return &posthogSource{cfg: c.PostHog, http: a.http}
}

func (a *App) notice(c *Config) string {
	if !a.demo && (c.PostHog.APIKey == "" || c.PostHog.Project == "") {
		if c.Web.Enabled {
			return "demo data: set the PostHog key at " + a.webURL(c)
		}
		return "demo data: set posthog.project and api_key in amber.json"
	}
	return ""
}

// requestSync asks the main loop for a refresh without blocking.
func (a *App) requestSync(force bool) {
	select {
	case a.syncs <- force:
	default:
	}
}

func (a *App) webURL(c *Config) string {
	if !c.Web.Enabled {
		return ""
	}
	return fmt.Sprintf("http://%s:%d  pin %s", localIP(), c.Web.Port, a.pin)
}

// state describes the device for the header and footer of a frame synced at `at`.
func (a *App) state(c *Config, at time.Time) screenState {
	st := screenState{battery: battery(), webURL: a.webURL(c), notice: a.notice(c)}
	interval := c.Refresh()
	switch {
	case a.sleeping(c):
		interval = time.Duration(c.Power.BatteryRefreshMinutes) * time.Minute
		st.power = fmt.Sprintf("on battery · sleeps, wakes every %d min", c.Power.BatteryRefreshMinutes)
	case charging():
		st.power = fmt.Sprintf("charging · live, every %d min", c.RefreshMinutes)
	default:
		st.power = fmt.Sprintf("live, every %d min", c.RefreshMinutes)
	}
	st.next = at.Add(interval)
	return st
}

// update fetches, renders and paints one frame. It reports false when the
// fetch could not happen or nothing came back, so the caller retries sooner.
func (a *App) update(force, full bool) bool {
	c := a.config()
	src := a.source(c)
	if src != nil {
		if state := wifiState(); state != "" && state != "CONNECTED" {
			a.mu.Lock()
			stats := a.stats
			a.mu.Unlock()
			st := a.state(c, time.Now())
			st.notice = "waiting for Wi-Fi (" + strings.ToLower(state) + ") ..."
			a.show(renderScreen(c, a.faces, a.store, stats, st), stats, full)
			if !ensureWiFi(90 * time.Second) {
				log.Printf("wifi did not connect, state %s", wifiState())
				return false
			}
		}
	}
	stats := a.store.Fetch(c, src, force, 0)
	img := renderScreen(c, a.faces, a.store, stats, a.state(c, stats.At))
	a.show(img, stats, full)
	return stats.Ran == 0 || stats.Failed < stats.Ran
}

func (a *App) show(img image.Image, stats Stats, full bool) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		log.Printf("png: %v", err)
		return
	}
	a.mu.Lock()
	a.stats, a.frame = stats, buf.Bytes()
	a.mu.Unlock()
	if err := paint(a.fbink, buf.Bytes(), full); err != nil {
		log.Printf("paint: %v", err)
	}
}

// ---------------------------------------------------------------- device

func battery() string {
	out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "battLevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// wifiState is the Kindle connection manager's state, "CONNECTED" when online,
// or "" when not on a Kindle.
func wifiState() string {
	out, err := exec.Command("lipc-get-prop", "com.lab126.wifid", "cmState").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ensureWiFi turns wireless on and waits for a connection. With the reader UI
// stopped, nothing else asks the Kindle to reconnect after a boot or a drop.
func ensureWiFi(limit time.Duration) bool {
	exec.Command("lipc-set-prop", "com.lab126.cmd", "wirelessEnable", "1").Run()
	exec.Command("lipc-set-prop", "com.lab126.wifid", "enable", "1").Run()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if wifiState() == "CONNECTED" {
			// Give DHCP and DNS a moment after the association.
			time.Sleep(3 * time.Second)
			return true
		}
	}
	return false
}

func localIP() string {
	for _, name := range []string{"wlan0", "en0"} {
		if ifc, err := net.InterfaceByName(name); err == nil {
			if addrs, err := ifc.Addrs(); err == nil {
				for _, addr := range addrs {
					if ipn, ok := addr.(*net.IPNet); ok && ipn.IP.To4() != nil {
						return ipn.IP.String()
					}
				}
			}
		}
	}
	return "localhost"
}

var fbinkMissing sync.Once

// paint shows a PNG frame. A full refresh flashes the panel to clear ghosting;
// a partial one updates in place. Without FBInk (on a laptop) it does nothing.
func paint(fbink string, frame []byte, full bool) error {
	if _, err := os.Stat(fbink); err != nil {
		fbinkMissing.Do(func() { log.Printf("%s not found, not painting (fine off-device)", fbink) })
		return nil
	}
	path := filepath.Join(os.TempDir(), "amber.png")
	if err := os.WriteFile(path, frame, 0o644); err != nil {
		return err
	}
	args := []string{"-q", "-g", "file=" + path}
	if full {
		args = append(args, "-f", "-W", "GC16")
	} else {
		args = append(args, "-W", "GL16")
	}
	cmd := exec.Command(fbink, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// watchTouch reads raw input events from the touchscreen. A short tap asks for a
// refresh; holding a finger down for three seconds asks to exit, which is the only
// way back to the reader UI without a computer, because the UI is stopped.
func watchTouch(device string, tap, exit chan<- struct{}) {
	f, err := os.Open(device)
	if err != nil {
		log.Printf("touch: %v", err)
		return
	}
	defer f.Close()
	const (
		evKey    = 0x01
		btnTouch = 0x14a
	)
	events := make(chan int32)
	go func() {
		// struct input_event on 32-bit ARM: timeval (2 x int32), type u16, code u16, value s32.
		buf := make([]byte, 16)
		for {
			if _, err := io.ReadFull(f, buf); err != nil {
				log.Printf("touch: %v", err)
				close(events)
				return
			}
			typ := binary.LittleEndian.Uint16(buf[8:])
			code := binary.LittleEndian.Uint16(buf[10:])
			if typ == evKey && code == btnTouch {
				events <- int32(binary.LittleEndian.Uint32(buf[12:]))
			}
		}
	}()
	var down time.Time
	hold := time.NewTimer(time.Hour)
	hold.Stop()
	for {
		select {
		case v, ok := <-events:
			if !ok {
				return
			}
			if v == 1 {
				down = time.Now()
				hold.Reset(3 * time.Second)
			} else if !down.IsZero() {
				hold.Stop()
				if time.Since(down) < time.Second {
					select {
					case tap <- struct{}{}:
					default:
					}
				}
				down = time.Time{}
			}
		case <-hold.C:
			exit <- struct{}{}
			return
		}
	}
}

func randomPIN() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(9000))
	return fmt.Sprintf("%04d", n.Int64()+1000)
}

func main() {
	configPath := flag.String("config", "amber.json", "path to amber.json")
	demo := flag.Bool("demo", false, "draw made-up data instead of querying PostHog (no API key needed)")
	pngPath := flag.String("png", "", "render one frame into this PNG file and exit")
	check := flag.Bool("check", false, "validate the config and exit")
	touchDevice := flag.String("touch", "/dev/input/event0", "touchscreen input device")
	fbink := flag.String("fbink", "/mnt/us/usbnet/bin/fbink", "path to the FBInk binary")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *check {
		fmt.Println("config ok")
		return
	}

	a := &App{
		path:  *configPath,
		demo:  *demo,
		fbink: *fbink,
		faces: loadFaces(),
		store: newStore(),
		http:  &http.Client{Timeout: 45 * time.Second},
		pin:   cfg.Web.PIN,
		syncs: make(chan bool, 1),
		cfg:   cfg,
	}
	if a.pin == "" {
		// Keep the PIN stable across restarts so it can be remembered.
		a.pin = randomPIN()
		cfg.Web.PIN = a.pin
		if *pngPath == "" {
			if err := saveConfig(*configPath, cfg); err != nil {
				log.Printf("saving the generated PIN: %v", err)
			}
		}
	}

	if *pngPath != "" {
		stats := a.store.Fetch(cfg, a.source(cfg), true, 0)
		st := a.state(cfg, stats.At)
		st.battery = "87"
		img := renderScreen(cfg, a.faces, a.store, stats, st)
		var buf bytes.Buffer
		png.Encode(&buf, img)
		if err := os.WriteFile(*pngPath, buf.Bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
		if stats.Failed > 0 {
			log.Fatalf("%d of %d queries failed, last: %s", stats.Failed, stats.Ran, stats.LastErr)
		}
		return
	}

	if cfg.Web.Enabled {
		go a.serveWeb()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	tap := make(chan struct{}, 1)
	exit := make(chan struct{})
	go watchTouch(*touchDevice, tap, exit)

	force := true
	// Stay awake for a few minutes after starting, so the web UI is reachable
	// right after a boot or a deploy even when running on battery.
	awakeUntil := time.Now().Add(3 * time.Minute)
	for n := 0; ; n++ {
		// Flash on start and every sixth update to clear ghosting.
		ok := a.update(force, n%6 == 0)
		if n == 0 {
			// The stopping reader UI can still paint its progress bar over the first
			// frame, so paint it once more after it has settled.
			time.Sleep(8 * time.Second)
			a.mu.Lock()
			frame := a.frame
			a.mu.Unlock()
			paint(a.fbink, frame, true)
		}
		force = !ok

		c := a.config()
		if a.sleeping(c) && !time.Now().Before(awakeUntil) {
			interval := time.Duration(c.Power.BatteryRefreshMinutes) * time.Minute
			if !ok && interval > 5*time.Minute {
				interval = 5 * time.Minute // offline or every query failed: try again sooner
			}
			if suspendFor(interval) {
				// Woken early, by the power button: stay up for the web UI.
				log.Printf("woken before the alarm, staying awake for 5 minutes")
				awakeUntil = time.Now().Add(5 * time.Minute)
			}
			continue
		}

		wait := c.Refresh()
		if !ok && wait > time.Minute {
			wait = time.Minute
		}
		if a.sleeping(c) {
			// Sync once more when the awake window ends, then go to sleep.
			wait = min(wait, time.Until(awakeUntil))
		}
		select {
		case <-stop:
			return
		case <-exit:
			exec.Command(a.fbink, "-q", "-c", "-f", "-m", "-M", "Starting the Kindle UI...").Run()
			return
		case <-tap:
			force = true
		case force = <-a.syncs:
		case <-time.After(wait):
		}
	}
}

// sleeping reports whether the Kindle should suspend between updates now.
func (a *App) sleeping(c *Config) bool {
	switch c.Power.Mode {
	case "awake":
		return false
	case "sleep":
		return onKindle()
	}
	return onKindle() && !charging()
}

func onKindle() bool {
	_, err := os.Stat("/sys/class/rtc/rtc1/wakealarm")
	return err == nil && wifiState() != ""
}

func charging() bool {
	out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "isCharging").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

// suspendFor puts the Kindle to sleep with an RTC alarm to wake it. The E Ink panel
// keeps showing the last frame without power. It reports whether something other
// than the alarm, such as the power button, woke the device early.
func suspendFor(d time.Duration) (early bool) {
	// Let the panel finish the refresh and flush the log before the SoC stops.
	time.Sleep(5 * time.Second)
	exec.Command("sync").Run()
	start := time.Now().Round(0) // wall clock: the monotonic clock stops during suspend
	log.Printf("sleeping for %s", d)
	// rtc1 is the SoC clock, the one enabled as a wakeup source on this Kindle.
	cmd := exec.Command("rtcwake", "-d", "rtc1", "-m", "mem", "-s", strconv.Itoa(int(d.Seconds())))
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("rtcwake: %v: %s; waiting awake instead", err, strings.TrimSpace(string(out)))
		time.Sleep(d)
		return false
	}
	slept := time.Now().Round(0).Sub(start)
	log.Printf("woke after %s", slept.Round(time.Second))
	return slept < d-time.Minute
}

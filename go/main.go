// lincstation-led: single static binary replacement for the vendor go_8130_led_linux service
// on the LincStation N1. Same HTTP API on port 50000, so the Home Assistant integration works
// unchanged. Standard library only; no i2c-tools, no shell, no external listener.
//
//	POST /rgb/mode    form: mode=off|normal|breath|loop  light=1..255  color0=#RRGGBB  color1=#RRGGBB
//	GET  /rgb/mode
//	POST /led/switch  form: enabled=1|0|true|false        (master switch for the status LEDs)
//	GET  /led/switch
//	POST /led/opt     form: targetLed=power|network|hhd0|hhd1|nvme0..nvme3  state=normal|warning|alert|off
//	GET  /health
//
// Register map recovered from the vendor binary: I2C bus 0, device 0x26.
//
//	0x90 mode (0 normal, 1 breath, 2 loop)   0x91 brightness
//	0x92-0x94 R,G,B (normal and breath)      0x95-0x97 colour 0, 0x98-0x9A colour 1 (loop)
//	Status LEDs: a white and a yellow bit each; 0xA0/0xA1 set bits, 0xB0/0xB1 clear bits.
//
// WARNING: the API has no authentication. Keep the port on a trusted network.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var version = "dev" // set at build time with -ldflags "-X main.version=..."

// ---------------------------------------------------------------- configuration

type config struct {
	port      int
	bind      string // listen address, empty means all interfaces
	bus       int
	addr      int
	netIf     string
	stateDir  string
	flashDir  string
	logFile   string
	i2cDevice string // override of /dev/i2c-N
	i2cLog    string // testing only: append "reg value" lines to this file instead of touching hardware
	flashSync time.Duration
	netPoll   time.Duration
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 0, 32); err == nil {
			return int(n)
		}
	}
	return def
}

func loadConfig() config {
	var c config
	var showVersion bool
	flag.IntVar(&c.port, "port", envInt("LED_PORT", 50000), "TCP port to listen on (env LED_PORT)")
	flag.StringVar(&c.bind, "bind", envStr("LED_BIND", ""), "address to listen on, e.g. 127.0.0.1 for local only (env LED_BIND, default all interfaces)")
	flag.IntVar(&c.bus, "bus", envInt("I2C_BUS", 0), "I2C bus number (env I2C_BUS)")
	flag.IntVar(&c.addr, "addr", envInt("I2C_ADDR", 0x26), "I2C device address (env I2C_ADDR)")
	flag.StringVar(&c.netIf, "net-if", envStr("NET_IF", "eth0"), "interface the network LED follows (env NET_IF)")
	flag.StringVar(&c.stateDir, "state-dir", envStr("STATE_DIR", "/var/local/lincstation-led"), "live state directory (env STATE_DIR)")
	flag.StringVar(&c.flashDir, "flash-dir", envStr("FLASH_DIR", "/boot/config/lincstation-led/state"), "persistent state directory (env FLASH_DIR)")
	flag.StringVar(&c.logFile, "log", envStr("LOG_FILE", "/var/log/lincstation-led.log"), "log file, or - for stderr (env LOG_FILE)")
	flag.StringVar(&c.i2cDevice, "i2c-dev", envStr("I2C_DEV", ""), "I2C device node (default /dev/i2c-<bus>)")
	flag.StringVar(&c.i2cLog, "i2c-log", envStr("I2C_LOG", ""), "testing only: log register writes to this file instead of using hardware")
	syncSecs := flag.Int("flash-sync", envInt("FLASH_SYNC_SECONDS", 300), "seconds between saves of changed state to flash (env FLASH_SYNC_SECONDS)")
	poll := flag.Int("net-poll", envInt("NET_POLL_SECONDS", 10), "seconds between network link checks")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
		fmt.Println("lincstation-led", version)
		os.Exit(0)
	}
	c.flashSync = time.Duration(*syncSecs) * time.Second
	c.netPoll = time.Duration(*poll) * time.Second
	if c.i2cDevice == "" {
		c.i2cDevice = fmt.Sprintf("/dev/i2c-%d", c.bus)
	}
	return c
}

// ---------------------------------------------------------------- I2C

type regWriter interface {
	Write(reg, val byte) error
}

const (
	i2cSlave         = 0x0703
	i2cSmbus         = 0x0720
	i2cSmbusWrite    = 0
	i2cSmbusByteData = 2
)

// Layout of struct i2c_smbus_ioctl_data from <linux/i2c-dev.h> (amd64 and arm64).
type smbusIoctlData struct {
	readWrite uint8
	command   uint8
	_         [2]byte
	size      uint32
	data      unsafe.Pointer
}

type i2cDevice struct {
	path string
	addr int
}

func ioctl(fd int, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg); e != 0 {
		return e
	}
	return nil
}

// Write performs an SMBus "write byte data", the same transfer as `i2cset -y BUS ADDR REG VALUE`.
func (d *i2cDevice) Write(reg, val byte) error {
	fd, err := syscall.Open(d.path, syscall.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", d.path, err)
	}
	defer syscall.Close(fd)
	if err := ioctl(fd, i2cSlave, uintptr(d.addr)); err != nil {
		return fmt.Errorf("set address 0x%02x: %w", d.addr, err)
	}
	var union [34]byte // union i2c_smbus_data
	union[0] = val
	args := smbusIoctlData{readWrite: i2cSmbusWrite, command: reg, size: i2cSmbusByteData, data: unsafe.Pointer(&union[0])}
	err = ioctl(fd, i2cSmbus, uintptr(unsafe.Pointer(&args)))
	_ = union // keep alive across the syscall
	if err != nil {
		return fmt.Errorf("write 0x%02x to reg 0x%02x: %w", val, reg, err)
	}
	return nil
}

type fileWriter struct {
	mu   sync.Mutex
	path string
}

func (f *fileWriter) Write(reg, val byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fmt.Fprintf(fh, "0x%02X %d\n", reg, val)
	return err
}

// ---------------------------------------------------------------- LED tables

type ledDef struct {
	set, clr byte // register that sets / clears bits
	white    byte
	yellow   byte
	blink    byte // first of two per-LED registers the vendor service zeroes at start
}

var ledTable = map[string]ledDef{
	"power":   {0xA0, 0xB0, 0x01, 0x02, 0x50},
	"network": {0xA0, 0xB0, 0x40, 0x80, 0x56},
	"hhd0":    {0xA0, 0xB0, 0x04, 0x08, 0x52},
	"hhd1":    {0xA0, 0xB0, 0x10, 0x20, 0x54},
	"nvme0":   {0xA1, 0xB1, 0x01, 0x02, 0x58},
	"nvme1":   {0xA1, 0xB1, 0x04, 0x08, 0x5A},
	"nvme2":   {0xA1, 0xB1, 0x10, 0x20, 0x5C},
	"nvme3":   {0xA1, 0xB1, 0x40, 0x80, 0x5E},
}

var ledOrder = []string{"power", "network", "hhd0", "hhd1", "nvme0", "nvme1", "nvme2", "nvme3"}

// ---------------------------------------------------------------- validation

var colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var lightRe = regexp.MustCompile(`^[0-9]{1,3}$`)

func validColor(s string) bool { return colorRe.MatchString(s) }

func validLight(s string) bool {
	if !lightRe.MatchString(s) {
		return false
	}
	n, _ := strconv.Atoi(s)
	return n >= 1 && n <= 255
}

func rgbBytes(hex string) (r, g, b byte) {
	n, _ := strconv.ParseUint(hex[1:], 16, 32)
	return byte(n >> 16), byte(n >> 8), byte(n)
}

// ---------------------------------------------------------------- service

type rgbState struct {
	Mode, Light, Color0, Color1 string
}

type server struct {
	gamma float64 // colour correction exponent for CLI mode (screen colours are gamma encoded, LED PWM is linear); 0 or 1 = none
	cfg   config
	dev   regWriter
	log   *log.Logger
	mu    sync.Mutex // serialises state files and hardware writes
}

// rgbOut converts a #RRGGBB colour into the three register values, applying colour correction.
func (s *server) rgbOut(hex string) (r, g, b byte) {
	r, g, b = rgbBytes(hex)
	if s.gamma <= 1 {
		return
	}
	fix := func(v byte) byte {
		if v == 0 {
			return 0
		}
		o := math.Round(255 * math.Pow(float64(v)/255, s.gamma))
		if o < 1 {
			o = 1
		}
		return byte(o)
	}
	return fix(r), fix(g), fix(b)
}

func (s *server) write(reg, val byte) {
	if err := s.dev.Write(reg, val); err != nil {
		s.log.Printf("i2c write failed (reg=0x%02X val=%d): %v", reg, val, err)
	}
}

// --- state files (same names and formats as the bash version, so the two are interchangeable)

func (s *server) path(name string) string { return filepath.Join(s.cfg.stateDir, name) }

func (s *server) readRGB() rgbState {
	st := rgbState{Mode: "breath", Color0: "#0A0B0C"} // stock first-boot defaults
	b, err := os.ReadFile(s.path("rgb"))
	if err != nil {
		return st
	}
	lines := strings.Split(string(b), "\n")
	for len(lines) < 4 {
		lines = append(lines, "")
	}
	return rgbState{Mode: lines[0], Light: lines[1], Color0: lines[2], Color1: lines[3]}
}

func (s *server) writeRGB(st rgbState) {
	data := fmt.Sprintf("%s\n%s\n%s\n%s\n", st.Mode, st.Light, st.Color0, st.Color1)
	if err := os.WriteFile(s.path("rgb"), []byte(data), 0o644); err != nil {
		s.log.Printf("cannot save rgb state: %v", err)
	}
}

func (s *server) ledsEnabled() bool {
	b, err := os.ReadFile(s.path("enabled"))
	return err != nil || strings.TrimSpace(string(b)) != "0"
}

func (s *server) setEnabled(on bool) {
	v := "0\n"
	if on {
		v = "1\n"
	}
	if err := os.WriteFile(s.path("enabled"), []byte(v), 0o644); err != nil {
		s.log.Printf("cannot save enabled state: %v", err)
	}
}

func (s *server) ledState(name string) string {
	b, err := os.ReadFile(s.path("led-" + name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// --- hardware operations (caller holds s.mu)

func (s *server) applyRGB(st rgbState) {
	switch st.Mode {
	case "off":
		s.write(0x90, 0)
		s.write(0x91, 0)
	case "normal":
		l, _ := strconv.Atoi(st.Light)
		r, g, b := s.rgbOut(st.Color0)
		s.write(0x90, 0)
		s.write(0x91, byte(l))
		s.write(0x92, r)
		s.write(0x93, g)
		s.write(0x94, b)
	case "breath":
		r, g, b := s.rgbOut(st.Color0)
		s.write(0x90, 1)
		s.write(0x92, r)
		s.write(0x93, g)
		s.write(0x94, b)
	case "loop":
		l, _ := strconv.Atoi(st.Light)
		r0, g0, b0 := s.rgbOut(st.Color0)
		r1, g1, b1 := s.rgbOut(st.Color1)
		s.write(0x90, 2)
		s.write(0x91, byte(l))
		s.write(0x95, r0)
		s.write(0x96, g0)
		s.write(0x97, b0)
		s.write(0x98, r1)
		s.write(0x99, g1)
		s.write(0x9A, b1)
	}
}

func (s *server) ledWrite(name, state string) {
	d := ledTable[name]
	switch state {
	case "normal":
		s.write(d.set, d.white)
		s.write(d.clr, d.yellow)
	case "warning":
		s.write(d.clr, d.white)
		s.write(d.set, d.yellow)
	case "alert":
		s.write(d.set, d.white)
		s.write(d.set, d.yellow)
	case "off":
		s.write(d.clr, d.white)
		s.write(d.clr, d.yellow)
	}
}

func (s *server) netState() string {
	b, err := os.ReadFile("/sys/class/net/" + s.cfg.netIf + "/carrier")
	if err == nil && strings.TrimSpace(string(b)) == "1" {
		return "normal"
	}
	return "warning"
}

// applyStatusLeds re-applies every remembered status LED. Power defaults to normal and the
// network LED follows the link unless it was set by hand. Drive LEDs are untouched until set.
func (s *server) applyStatusLeds() {
	for _, name := range ledOrder {
		st := s.ledState(name)
		if st == "" {
			switch name {
			case "power":
				st = "normal"
			case "network":
				st = "auto"
			default:
				continue
			}
		}
		if st == "auto" {
			st = s.netState()
		}
		s.ledWrite(name, st)
	}
}

func (s *server) allStatusLedsOff() {
	for _, name := range ledOrder {
		s.ledWrite(name, "off")
	}
}

func (s *server) initHardware() {
	for _, name := range ledOrder { // the vendor service zeroes the per-LED registers at start
		d := ledTable[name]
		s.write(d.blink, 0)
		s.write(d.blink+1, 0)
	}
}

func (s *server) restoreRGB() {
	st := s.readRGB()
	switch st.Mode {
	case "off":
		s.applyRGB(rgbState{Mode: "off"})
	case "breath":
		if validColor(st.Color0) {
			s.applyRGB(st)
		}
	case "normal":
		if validColor(st.Color0) && validLight(st.Light) {
			s.applyRGB(st)
		}
	case "loop":
		if validColor(st.Color0) && validColor(st.Color1) && validLight(st.Light) {
			s.applyRGB(st)
		}
	}
}

// ---------------------------------------------------------------- persistence

func (s *server) stateFiles(dir string) []string {
	files := []string{"rgb", "enabled"}
	for _, n := range ledOrder {
		files = append(files, "led-"+n)
	}
	var out []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func (s *server) restoreFromFlash() {
	_ = os.MkdirAll(s.cfg.stateDir, 0o755)
	for _, f := range s.stateFiles(s.cfg.flashDir) {
		dst := s.path(f)
		if _, err := os.Stat(dst); err != nil {
			_ = copyFile(filepath.Join(s.cfg.flashDir, f), dst)
		}
	}
}

// syncToFlash copies changed state files to flash, writing only when something differs
// (the flash drive is a USB stick, so unnecessary writes are avoided).
func (s *server) syncToFlash() {
	if _, err := os.Stat(filepath.Dir(s.cfg.flashDir)); err != nil {
		return
	}
	_ = os.MkdirAll(s.cfg.flashDir, 0o755)
	for _, f := range s.stateFiles(s.cfg.stateDir) {
		a, errA := os.ReadFile(s.path(f))
		b, errB := os.ReadFile(filepath.Join(s.cfg.flashDir, f))
		if errA == nil && (errB != nil || string(a) != string(b)) {
			if err := os.WriteFile(filepath.Join(s.cfg.flashDir, f), a, 0o644); err != nil {
				s.log.Printf("flash sync failed for %s: %v", f, err)
			}
		}
	}
}

// ---------------------------------------------------------------- HTTP

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type msg struct {
	Message string `json:"message"`
}
type msgCap struct {
	Message string `json:"Message"`
}
type switchResp struct {
	Enabled bool   `json:"enabled"`
	Message string `json:"message"`
}
type rgbResp struct {
	Mode   string `json:"Mode"`
	Light  string `json:"Light"`
	Color0 string `json:"Color0"`
	Color1 string `json:"Color1"`
}

func postForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, msg{"Invalid parameters"})
		return false
	}
	return true
}

func (s *server) routes() http.Handler {
	table := map[string]map[string]func(http.ResponseWriter, *http.Request){}
	handle := func(path, method string, h func(http.ResponseWriter, *http.Request)) {
		if table[path] == nil {
			table[path] = map[string]func(http.ResponseWriter, *http.Request){}
		}
		table[path][method] = h
	}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := table[r.URL.Path][r.Method]; ok {
			h(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, msg{"not found"})
	})

	handle("/health", http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, msg{"ok"})
	})

	handle("/rgb/mode", http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		st := s.readRGB()
		s.mu.Unlock()
		writeJSON(w, 200, rgbResp(st))
	})

	handle("/rgb/mode", http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if !postForm(w, r) {
			return
		}
		st := rgbState{
			Mode:   r.PostForm.Get("mode"),
			Light:  r.PostForm.Get("light"),
			Color0: r.PostForm.Get("color0"),
			Color1: r.PostForm.Get("color1"),
		}
		ok := false
		switch st.Mode {
		case "off":
			ok = true
		case "normal":
			ok = validColor(st.Color0) && validLight(st.Light)
		case "breath":
			ok = validColor(st.Color0)
		case "loop":
			ok = validColor(st.Color0) && validColor(st.Color1) && validLight(st.Light)
		}
		if !ok {
			writeJSON(w, http.StatusBadRequest, msg{"Invalid parameters"})
			return
		}
		// Only validated values are stored; anything else is dropped.
		if !validLight(st.Light) {
			st.Light = ""
		}
		if !validColor(st.Color0) {
			st.Color0 = ""
		}
		if !validColor(st.Color1) {
			st.Color1 = ""
		}
		s.mu.Lock()
		s.writeRGB(st)
		s.applyRGB(st)
		s.mu.Unlock()
		writeJSON(w, 200, msg{"Success"})
	})

	handle("/led/switch", http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		on := s.ledsEnabled()
		s.mu.Unlock()
		writeJSON(w, 200, switchResp{on, "Get led switch success"})
	})

	handle("/led/switch", http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if !postForm(w, r) {
			return
		}
		on, err := strconv.ParseBool(r.PostForm.Get("enabled"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, msg{"Invalid boolean value"})
			return
		}
		s.mu.Lock()
		s.setEnabled(on)
		if on {
			s.applyStatusLeds()
		} else {
			s.allStatusLedsOff()
		}
		s.mu.Unlock()
		writeJSON(w, 200, switchResp{on, "Post led switch success"})
	})

	handle("/led/state", http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		out := map[string]string{}
		for _, name := range ledOrder {
			out[name] = s.ledState(name) // "" until set; the network LED reports "auto" by default
			if out[name] == "" && name == "network" {
				out[name] = "auto"
			}
			if out[name] == "" && name == "power" {
				out[name] = "normal"
			}
		}
		s.mu.Unlock()
		writeJSON(w, 200, out)
	})

	handle("/led/opt", http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		if !postForm(w, r) {
			return
		}
		target, state := r.PostForm.Get("targetLed"), r.PostForm.Get("state")
		if _, ok := ledTable[target]; !ok {
			writeJSON(w, http.StatusInternalServerError, msgCap{"Target not exist"})
			return
		}
		switch state {
		case "normal", "warning", "alert", "off":
		default:
			writeJSON(w, http.StatusBadRequest, msgCap{"Invalid state"})
			return
		}
		s.mu.Lock()
		if err := os.WriteFile(s.path("led-"+target), []byte(state+"\n"), 0o644); err != nil {
			s.log.Printf("cannot save led state: %v", err)
		}
		if s.ledsEnabled() {
			s.ledWrite(target, state)
		}
		s.mu.Unlock()
		writeJSON(w, 200, msgCap{"Opt led success"})
	})

	return mux
}

// ---------------------------------------------------------------- main

func (s *server) background(ctx context.Context) {
	lastNet := ""
	if s.ledsEnabled() && s.ledState("network") == "" {
		lastNet = s.netState()
	}
	netTick := time.NewTicker(s.cfg.netPoll)
	flashTick := time.NewTicker(s.cfg.flashSync)
	defer netTick.Stop()
	defer flashTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-netTick.C:
			s.mu.Lock()
			if st := s.ledState("network"); s.ledsEnabled() && (st == "" || st == "auto") {
				if cur := s.netState(); cur != lastNet {
					s.ledWrite("network", cur)
					lastNet = cur
				}
			}
			s.mu.Unlock()
		case <-flashTick.C:
			s.mu.Lock()
			s.syncToFlash()
			s.mu.Unlock()
		}
	}
}

func ensureI2CNode(cfg config, lg *log.Logger) {
	if cfg.i2cLog != "" {
		return
	}
	if _, err := os.Stat(cfg.i2cDevice); err == nil {
		return
	}
	if out, err := exec.Command("modprobe", "i2c-dev").CombinedOutput(); err != nil {
		lg.Printf("modprobe i2c-dev failed: %v %s", err, strings.TrimSpace(string(out)))
	}
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(cfg.i2cDevice); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	lg.Printf("warning: %s does not exist (i2c-dev not loaded?)", cfg.i2cDevice)
}

func main() {
	if isCLI(os.Args) {
		os.Exit(runCLI(os.Args))
	}
	cfg := loadConfig()

	var out io.Writer = os.Stderr
	if cfg.logFile != "-" {
		if f, err := os.OpenFile(cfg.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			out = f
		} else {
			fmt.Fprintf(os.Stderr, "cannot open log %s: %v (logging to stderr)\n", cfg.logFile, err)
		}
	}
	lg := log.New(out, "", log.LstdFlags)

	s := &server{cfg: cfg, log: lg}
	if cfg.i2cLog != "" {
		s.dev = &fileWriter{path: cfg.i2cLog}
	} else {
		s.dev = &i2cDevice{path: cfg.i2cDevice, addr: cfg.addr}
	}
	if err := os.MkdirAll(cfg.stateDir, 0o755); err != nil {
		lg.Fatalf("cannot create state dir %s: %v", cfg.stateDir, err)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.bind, strconv.Itoa(cfg.port)))
	if err != nil {
		lg.Fatalf("cannot listen on port %d: %v", cfg.port, err)
	}
	lg.Printf("lincstation-led %s starting on %s (device %s addr 0x%02X)", version, ln.Addr(), cfg.i2cDevice, cfg.addr)

	ensureI2CNode(cfg, lg)
	s.restoreFromFlash()
	s.mu.Lock()
	s.initHardware()
	s.restoreRGB()
	if s.ledsEnabled() {
		s.applyStatusLeds()
	}
	s.mu.Unlock()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go s.background(ctx)

	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          lg,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		lg.Fatalf("server error: %v", err)
	}
	s.mu.Lock()
	s.syncToFlash()
	s.mu.Unlock()
	lg.Printf("stopped")
}

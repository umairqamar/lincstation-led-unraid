package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// One-shot command line mode: no listener, no daemon. Used by the Unraid plugin.
//
//	lincstation-led set -mode normal -color0 #87CEEB -light 200
//	lincstation-led get          (prints the saved light bar state as JSON)
//	lincstation-led restore      (re-applies the saved state, used at boot)
func isCLI(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "set", "get", "restore":
		return true
	}
	return false
}

func runCLI(args []string) int {
	cmd := args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		stateDir = fs.String("state-dir", envStr("STATE_DIR", "/boot/config/plugins/lincstation-led/state"), "directory holding the saved state")
		bus      = fs.Int("bus", envInt("I2C_BUS", 0), "I2C bus number")
		addr     = fs.Int("addr", envInt("I2C_ADDR", 0x26), "I2C device address")
		netIf    = fs.String("net-if", envStr("NET_IF", "eth0"), "interface the network LED follows at restore")
		i2cDev   = fs.String("i2c-dev", "", "I2C device node (default /dev/i2c-<bus>)")
		i2cLog   = fs.String("i2c-log", envStr("I2C_LOG", ""), "testing only: log register writes to this file")
		logFile  = fs.String("log", "/var/log/lincstation-led.log", "log file, or - for stderr")
		mode     = fs.String("mode", "", "off | normal | breath | loop")
		light    = fs.String("light", "", "brightness 1-255 (normal and loop)")
		color0   = fs.String("color0", "", "#RRGGBB")
		color1   = fs.String("color1", "", "#RRGGBB (loop only)")
		gamma    = fs.String("gamma", "", "colour correction exponent 1-3 (1 = off); saved and used by later runs")
	)
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	dev := *i2cDev
	if dev == "" {
		dev = fmt.Sprintf("/dev/i2c-%d", *bus)
	}
	cfg := config{bus: *bus, addr: *addr, netIf: *netIf, stateDir: *stateDir, i2cDevice: dev, i2cLog: *i2cLog, logFile: *logFile}

	var out io.Writer = os.Stderr
	if cfg.logFile != "-" {
		if f, err := os.OpenFile(cfg.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			defer f.Close()
			out = f
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
		fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", cfg.stateDir, err)
		return 1
	}

	// colour correction: saved in <state-dir>/gamma, default 2.2 so colours look like they do on screen
	if *gamma != "" {
		g, err := strconv.ParseFloat(*gamma, 64)
		if err != nil || g < 1 || g > 3 {
			fmt.Fprintln(os.Stderr, "gamma must be between 1 and 3")
			return 2
		}
		s.gamma = g
	}

	// serialise concurrent invocations (two browser tabs, boot restore racing a click)
	lock, err := os.OpenFile(filepath.Join(cfg.stateDir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		defer lock.Close()
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	}

	saved := 2.2
	if b, err := os.ReadFile(filepath.Join(cfg.stateDir, "gamma")); err == nil {
		if g, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && g >= 1 && g <= 3 {
			saved = g
		}
	}
	if s.gamma == 0 {
		s.gamma = saved
	} else if s.gamma != saved || cmd == "set" {
		_ = os.WriteFile(filepath.Join(cfg.stateDir, "gamma"), []byte(strconv.FormatFloat(s.gamma, 'f', -1, 64)+"\n"), 0o644)
	}

	switch cmd {
	case "get":
		st := s.readRGB()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Mode": st.Mode, "Light": st.Light, "Color0": st.Color0, "Color1": st.Color1, "Gamma": strconv.FormatFloat(s.gamma, 'f', -1, 64)})
		return 0

	case "restore":
		ensureI2CNode(cfg, lg)
		s.initHardware()
		s.restoreRGB()
		// power LED on, network LED follows the link once (there is no daemon to track changes)
		s.ledWrite("power", "normal")
		s.ledWrite("network", s.netState())
		lg.Printf("restored light state (%s)", s.readRGB().Mode)
		return 0

	case "set":
		st := rgbState{Mode: *mode, Light: *light, Color0: *color0, Color1: *color1}
		switch st.Mode {
		case "off":
			st = rgbState{Mode: "off"}
		case "normal":
			if !validColor(st.Color0) || !validLight(st.Light) {
				fmt.Fprintln(os.Stderr, "normal needs -color0 #RRGGBB and -light 1-255")
				return 2
			}
			st.Color1 = ""
		case "breath":
			if !validColor(st.Color0) {
				fmt.Fprintln(os.Stderr, "breath needs -color0 #RRGGBB")
				return 2
			}
			if !validLight(st.Light) {
				st.Light = ""
			}
			st.Color1 = ""
		case "loop":
			if !validColor(st.Color0) || !validColor(st.Color1) || !validLight(st.Light) {
				fmt.Fprintln(os.Stderr, "loop needs -color0, -color1 and -light 1-255")
				return 2
			}
		default:
			fmt.Fprintln(os.Stderr, "mode must be off, normal, breath or loop")
			return 2
		}
		ensureI2CNode(cfg, lg)
		s.applyRGB(st)
		old := s.readRGB()
		if old != st { // only touch the flash drive when something changed
			s.writeRGB(st)
		}
		return 0
	}
	return 2
}

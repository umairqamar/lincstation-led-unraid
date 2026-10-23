package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run executes the CLI against a temporary state directory with register writes logged to a file.
func run(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	log := filepath.Join(dir, "i2c")
	_ = os.Remove(log)
	full := append([]string{"lincstation-led"}, args...)
	full = append(full, "-state-dir", filepath.Join(dir, "state"), "-i2c-log", log, "-log", filepath.Join(dir, "log"))
	code := runCLI(full)
	b, _ := os.ReadFile(log)
	return code, strings.Join(strings.Fields(strings.ReplaceAll(string(b), "\n", ";")), " ")
}

func TestSetNormalGammaOff(t *testing.T) {
	dir := t.TempDir()
	code, regs := run(t, dir, "set", "-mode", "normal", "-color0", "#87CEEB", "-light", "200", "-gamma", "1")
	if code != 0 || regs != "0x90 0;0x91 200;0x92 135;0x93 206;0x94 235;" {
		t.Fatalf("code=%d regs=%q", code, regs)
	}
}

func TestSetNormalGammaCorrected(t *testing.T) {
	dir := t.TempDir()
	code, regs := run(t, dir, "set", "-mode", "normal", "-color0", "#87CEEB", "-light", "200", "-gamma", "2.2")
	if code != 0 || regs != "0x90 0;0x91 200;0x92 63;0x93 159;0x94 213;" {
		t.Fatalf("code=%d regs=%q", code, regs)
	}
}

func TestLoopAndBreathAndOff(t *testing.T) {
	dir := t.TempDir()
	if _, regs := run(t, dir, "set", "-mode", "loop", "-color0", "#112233", "-color1", "#445566", "-light", "90", "-gamma", "1"); !strings.HasPrefix(regs, "0x90 2;0x91 90;0x95 17;0x96 34;0x97 51;0x98 68;0x99 85;0x9A 102;") {
		t.Fatalf("loop regs=%q", regs)
	}
	if _, regs := run(t, dir, "set", "-mode", "breath", "-color0", "#FF0000", "-gamma", "1"); regs != "0x90 1;0x92 255;0x93 0;0x94 0;" {
		t.Fatalf("breath regs=%q", regs)
	}
	if _, regs := run(t, dir, "set", "-mode", "off"); regs != "0x90 0;0x91 0;" {
		t.Fatalf("off regs=%q", regs)
	}
}

func TestRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	bad := [][]string{
		{"set", "-mode", "normal", "-color0", "zzz", "-light", "5"},
		{"set", "-mode", "normal", "-color0", "#112233", "-light", "0"},
		{"set", "-mode", "normal", "-color0", "#112233", "-light", "256"},
		{"set", "-mode", "loop", "-color0", "#112233", "-light", "5"},
		{"set", "-mode", "disco", "-color0", "#112233", "-light", "5"},
		{"set", "-mode", "normal", "-color0", "#112233", "-light", "5", "-gamma", "9"},
	}
	for _, a := range bad {
		code, regs := run(t, dir, a...)
		if code == 0 || regs != "" {
			t.Errorf("%v: code=%d regs=%q (expected rejection and no register writes)", a, code, regs)
		}
	}
}

func TestStateSurvivesAndRestores(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "set", "-mode", "normal", "-color0", "#0000FF", "-light", "50", "-gamma", "1")
	code, regs := run(t, dir, "restore", "-net-if", "lo")
	if code != 0 || !strings.Contains(regs, "0x90 0;0x91 50;0x92 0;0x93 0;0x94 255;") {
		t.Fatalf("restore code=%d regs=%q", code, regs)
	}
}

func TestRgbOutNeverZeroesNonZero(t *testing.T) {
	s := &server{gamma: 2.2}
	r, g, b := s.rgbOut("#010000")
	if r != 1 || g != 0 || b != 0 {
		t.Fatalf("got %d %d %d", r, g, b)
	}
}

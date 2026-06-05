package main

import (
	"bytes"
	"os"
	"testing"
)

// unsetNoColor removes NO_COLOR for the duration of the test so the TTY branch
// of colorEnabled is exercised rather than the NO_COLOR short-circuit.
func unsetNoColor(t *testing.T) {
	t.Helper()
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatalf("Unsetenv NO_COLOR: %v", err)
	}
}

func TestColorEnabled(t *testing.T) {
	t.Run("NO_COLOR disables even for a char device", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		// A regular file is never a char device, but assert NO_COLOR short-circuits regardless.
		if colorEnabled(os.Stdin) {
			t.Errorf("colorEnabled: want false with NO_COLOR set, got true")
		}
	})

	t.Run("pipe is not a terminal", func(t *testing.T) {
		unsetNoColor(t)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		defer func() { _ = r.Close() }()
		defer func() { _ = w.Close() }()
		if colorEnabled(w) {
			t.Errorf("colorEnabled: want false for a pipe, got true")
		}
	})

	t.Run("regular file is not a terminal", func(t *testing.T) {
		unsetNoColor(t)
		f, err := os.CreateTemp(t.TempDir(), "color")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		defer func() { _ = f.Close() }()
		if colorEnabled(f) {
			t.Errorf("colorEnabled: want false for a regular file, got true")
		}
	})
}

func TestPainter(t *testing.T) {
	on := &painter{enabled: true}
	off := &painter{enabled: false}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"header on", on.header("h"), ansiBold + ansiCyan + "h" + ansiReset},
		{"delim on", on.delim("d"), ansiDim + "d" + ansiReset},
		{"info on", on.info("i"), ansiCyan + "i" + ansiReset},
		{"success on", on.success("s"), ansiGreen + "s" + ansiReset},
		{"warn on", on.warn("w"), ansiYellow + "w" + ansiReset},
		{"failure on", on.failure("f"), ansiRed + "f" + ansiReset},
		{"header off", off.header("h"), "h"},
		{"delim off", off.delim("d"), "d"},
		{"info off", off.info("i"), "i"},
		{"success off", off.success("s"), "s"},
		{"warn off", off.warn("w"), "w"},
		{"failure off", off.failure("f"), "f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("painter.%s: want %q, got %q", tt.name, tt.want, tt.got)
			}
		})
	}

	t.Run("nil painter is a passthrough", func(t *testing.T) {
		var p *painter
		if got := p.header("x"); got != "x" {
			t.Errorf("painter.header(nil): want %q, got %q", "x", got)
		}
	})
}

func TestLineColorWriter(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		in      string
		want    string
	}{
		{"trailing newline wrapped before newline", true, "msg\n", ansiRed + "msg" + ansiReset + "\n"},
		{"no trailing newline", true, "msg", ansiRed + "msg" + ansiReset},
		{"empty input passthrough", true, "", ""},
		{"disabled passthrough", false, "msg\n", "msg\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cw := &lineColorWriter{w: &buf, code: ansiRed, enabled: tt.enabled}
			n, err := cw.Write([]byte(tt.in))
			if err != nil {
				t.Fatalf("lineColorWriter.Write: unexpected error: %v", err)
			}
			if n != len(tt.in) {
				t.Errorf("lineColorWriter.Write: want n=%d, got %d", len(tt.in), n)
			}
			if buf.String() != tt.want {
				t.Errorf("lineColorWriter.Write: want %q, got %q", tt.want, buf.String())
			}
		})
	}
}

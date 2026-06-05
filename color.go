package main

import (
	"io"
	"os"
)

// ANSI escape codes for coloring the orchestrator's own output. The LLM stream
// is intentionally left uncolored so the two are visually distinct.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// colorEnabled reports whether ANSI coloring should be applied to f. Coloring is
// disabled when NO_COLOR is set (https://no-color.org) or when f is not a
// terminal (e.g. a pipe or regular file), so piped output stays clean.
func colorEnabled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// painter wraps strings in semantic ANSI colors. When disabled (or nil), every
// method returns its argument unchanged.
type painter struct {
	enabled bool
}

func newPainter(f *os.File) *painter {
	return &painter{enabled: colorEnabled(f)}
}

func (p *painter) wrap(codes, s string) string {
	if p == nil || !p.enabled {
		return s
	}
	return codes + s + ansiReset
}

func (p *painter) header(s string) string  { return p.wrap(ansiBold+ansiCyan, s) }
func (p *painter) delim(s string) string   { return p.wrap(ansiDim, s) }
func (p *painter) info(s string) string    { return p.wrap(ansiCyan, s) }
func (p *painter) success(s string) string { return p.wrap(ansiGreen, s) }
func (p *painter) warn(s string) string    { return p.wrap(ansiYellow, s) }
func (p *painter) failure(s string) string { return p.wrap(ansiRed, s) }

// lineColorWriter wraps each Write in code...ansiReset, keeping a trailing
// newline outside the reset so the colored region ends with the line content.
// It is the single chokepoint for coloring the standard logger's output.
type lineColorWriter struct {
	w       io.Writer
	code    string
	enabled bool
}

func (c *lineColorWriter) Write(p []byte) (int, error) {
	if !c.enabled || len(p) == 0 {
		return c.w.Write(p)
	}
	body, suffix := p, []byte(nil)
	if body[len(body)-1] == '\n' {
		body, suffix = body[:len(body)-1], []byte("\n")
	}
	out := make([]byte, 0, len(c.code)+len(body)+len(ansiReset)+len(suffix))
	out = append(out, c.code...)
	out = append(out, body...)
	out = append(out, ansiReset...)
	out = append(out, suffix...)
	if _, err := c.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

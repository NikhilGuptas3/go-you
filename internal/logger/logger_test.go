package logger

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// captureHandler is a test double: same rendering as textHandler but to an
// in-memory buffer, so we can assert the exact line shape without touching
// os.Stderr.
func newCapture(level slog.Level) (*slog.Logger, *bytes.Buffer, func(slog.Level)) {
	buf := &bytes.Buffer{}
	lv := new(slog.LevelVar)
	lv.Set(level)
	h := &captureTextHandler{buf: buf, level: lv, mu: &sync.Mutex{}}
	return slog.New(h), buf, lv.Set
}

// captureTextHandler mirrors textHandler.Handle but writes to a buffer.
type captureTextHandler struct {
	buf       *bytes.Buffer
	level     *slog.LevelVar
	mu        *sync.Mutex
	component string
	attrs     string
}

func (h *captureTextHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *captureTextHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format(timeFormat))
	b.WriteString(" - ")
	b.WriteString(levelString(r.Level))
	b.WriteString(" - ")
	b.WriteString("42") // fixed goroutine id for a deterministic test
	b.WriteString(" - ")
	comp := h.component
	if comp == "" {
		comp = "go-you"
	}
	b.WriteString(comp)
	b.WriteString(" - ")
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf.WriteString(b.String())
	return nil
}

func (h *captureTextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range as {
		writeAttr(&b, a)
	}
	nh := *h
	nh.attrs = b.String()
	return &nh
}
func (h *captureTextHandler) WithGroup(string) slog.Handler { return h }

func TestLineShape(t *testing.T) {
	l, buf, _ := newCapture(slog.LevelInfo)
	l.Info("persona handled", "tenant", "sign3-outside-3", "status", 200, "rid", "abc")
	got := buf.String()

	// time - LEVEL - 42 - component - msg key=value…
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} - INFO - 42 - go-you - persona handled tenant=sign3-outside-3 status=200 rid=abc\n$`)
	if !re.MatchString(got) {
		t.Fatalf("line shape mismatch:\n%q", got)
	}
}

func TestLevelSuppression(t *testing.T) {
	l, buf, _ := newCapture(slog.LevelInfo)
	l.Debug("should be suppressed")
	if buf.Len() != 0 {
		t.Fatalf("debug leaked at info level: %q", buf.String())
	}
	l.Info("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Fatalf("info not emitted: %q", buf.String())
	}
}

func TestLevelDebugEnables(t *testing.T) {
	l, buf, _ := newCapture(slog.LevelDebug)
	l.Debug("debug line")
	if !strings.Contains(buf.String(), "DEBUG") || !strings.Contains(buf.String(), "debug line") {
		t.Fatalf("debug not emitted at debug level: %q", buf.String())
	}
}

func TestQuoteIfNeeded(t *testing.T) {
	l, buf, _ := newCapture(slog.LevelInfo)
	l.Info("msg", "reason", "phone or email required")
	if !strings.Contains(buf.String(), `reason="phone or email required"`) {
		t.Fatalf("value with spaces not quoted: %q", buf.String())
	}
}

func TestLevelString(t *testing.T) {
	cases := map[slog.Level]string{
		slog.LevelDebug: "DEBUG",
		slog.LevelInfo:  "INFO",
		slog.LevelWarn:  "WARNING",
		slog.LevelError: "ERROR",
	}
	for lv, want := range cases {
		if got := levelString(lv); got != want {
			t.Errorf("levelString(%v) = %q, want %q", lv, got, want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug,
		"info": slog.LevelInfo, "": slog.LevelInfo, "bogus": slog.LevelInfo,
		"warn": slog.LevelWarn, "error": slog.LevelError,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestShortFunc(t *testing.T) {
	cases := map[string]string{
		"github.com/sign3labs/go-you/internal/handler.(*Persona).Handle": "Handle",
		"main.main": "main",
		"plain":     "plain",
	}
	for in, want := range cases {
		if got := shortFunc(in); got != want {
			t.Errorf("shortFunc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestIDContext(t *testing.T) {
	ctx := context.Background()
	if RequestIDFromContext(ctx) != "" {
		t.Fatal("empty context should yield empty rid")
	}
	ctx = WithRequestID(ctx, "rid-123")
	if got := RequestIDFromContext(ctx); got != "rid-123" {
		t.Fatalf("rid round-trip = %q, want rid-123", got)
	}
	// Empty id must not shadow a parent value.
	same := WithRequestID(ctx, "")
	if got := RequestIDFromContext(same); got != "rid-123" {
		t.Fatalf("empty WithRequestID clobbered parent: %q", got)
	}
}

// Init installs the real handler on the global default; smoke-test it writes.
func TestInitInstalls(t *testing.T) {
	// Redirect os.Stderr is heavy; instead just confirm Init returns a logger
	// and the default level honours the arg.
	Init("debug")
	if defaultLevel.Level() != slog.LevelDebug {
		t.Fatalf("Init(debug) level = %v", defaultLevel.Level())
	}
	SetLevel("error")
	if defaultLevel.Level() != slog.LevelError {
		t.Fatalf("SetLevel(error) level = %v", defaultLevel.Level())
	}
	// Component returns a tagged logger without panicking.
	_ = Component("test")
	// Reset so other packages' tests see info.
	SetLevel("info")
	_ = os.Stderr
}

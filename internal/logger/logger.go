// Package logger provides go-you's application logger. It mirrors the Python
// hey-you setup (stdlib logging → plain text on stderr) rather than emitting
// JSON, so the line shape matches what operators already read from hey-you:
//
//	2006-01-02 15:04:05.000 - LEVEL - <goroutine> - <component>:<func> - <message> key=value …
//
// The Python format is
//
//	%(asctime)s - %(levelname)s - %(thread)d - %(module)s:%(funcName)s - %(message)s
//
// (engine/application.py:20-22). We reproduce that column layout: the WSGI
// thread id becomes a goroutine id, and module:func becomes a caller-supplied
// component plus the calling function (resolved via runtime.Caller). Unlike
// hey-you — which interpolates everything into the message and carries no
// request id — we append structured attrs as key=value, so request_id / tenant /
// latency are greppable.
//
// It is built on log/slog (Go 1.24 stdlib, no new dependency): Init installs a
// custom text handler as the slog default, and the package-level helpers plus
// component loggers write through it.
package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Level names accepted by Init / LOG_LEVEL.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// timeFormat matches Python logging's default asctime "%Y-%m-%d %H:%M:%S,mmm"
// but uses '.' for the millisecond separator (Go's reference-time layout cannot
// emit a comma before fractional seconds). The column is otherwise identical.
const timeFormat = "2006-01-02 15:04:05.000"

// defaultLevel is the process log level, adjustable via Init. It is an
// slog.LevelVar so a handler installed once keeps honouring level changes.
var defaultLevel = new(slog.LevelVar)

// Init configures the global slog default logger to write hey-you-style plain
// text to stderr at the given level ("debug"/"info"/"warn"/"error"; anything
// unrecognised, including "", falls back to info). It is safe to call once at
// startup. Returns the installed *slog.Logger for convenience.
func Init(level string) *slog.Logger {
	defaultLevel.Set(parseLevel(level))
	h := &textHandler{
		out:   os.Stderr,
		level: defaultLevel,
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

// parseLevel maps a LOG_LEVEL string to an slog.Level, defaulting to Info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	case LevelInfo, "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// SetLevel adjusts the process log level at runtime (used by tests).
func SetLevel(level string) { defaultLevel.Set(parseLevel(level)) }

// DebugEnabled reports whether the process level is debug. Use it to guard
// expensive debug-only work (payload marshalling/truncation) so it is skipped
// unless debug logging is actually on:
//
//	if debugEnv || logger.DebugEnabled() { ... marshal ... logger.Debug(...) }
func DebugEnabled() bool { return defaultLevel.Level() <= slog.LevelDebug }

// textHandler renders records in hey-you's column layout. It is deliberately
// minimal: one writer, one shared level, and a list of preformatted attrs
// carried by With(). Writes are serialised so concurrent goroutines (the
// crawler fan-out) never interleave a line.
type textHandler struct {
	out       *os.File
	level     *slog.LevelVar
	mu        *sync.Mutex // shared across With()-derived handlers so all writes serialise
	component string
	attrs     string // preformatted " k=v k=v" carried from With()
}

var handlerMu sync.Mutex // the one real mutex; every textHandler points mu at it

func (h *textHandler) lock() *sync.Mutex {
	if h.mu != nil {
		return h.mu
	}
	return &handlerMu
}

func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format(timeFormat))
	b.WriteString(" - ")
	b.WriteString(levelString(r.Level))
	b.WriteString(" - ")
	b.WriteString(goroutineID())
	b.WriteString(" - ")
	b.WriteString(h.componentAndFunc(r))
	b.WriteString(" - ")
	b.WriteString(r.Message)

	// Attrs carried from With() first, then the record's own, each as " k=v".
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	mu := h.lock()
	mu.Lock()
	defer mu.Unlock()
	_, err := h.out.WriteString(b.String())
	return err
}

// componentAndFunc renders the "module:func" column. The component is the
// caller-supplied label (e.g. "persona"); the func is resolved from the record
// PC when available (slog captures it), else omitted.
func (h *textHandler) componentAndFunc(r slog.Record) string {
	comp := h.component
	if comp == "" {
		comp = "go-you"
	}
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		if f, _ := fs.Next(); f.Function != "" {
			return comp + ":" + shortFunc(f.Function)
		}
	}
	return comp
}

func (h *textHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range as {
		writeAttr(&b, a)
	}
	nh := *h
	nh.mu = h.lock()
	nh.attrs = b.String()
	return &nh
}

func (h *textHandler) WithGroup(name string) slog.Handler {
	// Groups are not used by go-you's flat key=value convention; ignore the
	// name and keep the same handler (attrs still accumulate via WithAttrs).
	return h
}

// writeAttr appends " key=value" to b, quoting values that contain spaces or
// '=' so the line stays parseable.
func writeAttr(b *strings.Builder, a slog.Attr) {
	if a.Key == "" {
		return
	}
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	b.WriteString(quoteIfNeeded(a.Value.String()))
}

func quoteIfNeeded(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\"=") {
		return strconv.Quote(v)
	}
	return v
}

// levelString renders the level like Python's %(levelname)s (upper case),
// mapping slog's DEBUG/INFO/WARN/ERROR to the same tokens.
func levelString(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING" // Python uses WARNING, not WARN
	default:
		return "ERROR"
	}
}

// shortFunc trims a fully-qualified func name
// (github.com/sign3labs/go-you/internal/handler.(*Persona).Handle) down to the
// trailing identifier (Handle), matching Python's %(funcName)s.
func shortFunc(full string) string {
	if i := strings.LastIndexByte(full, '.'); i >= 0 {
		return full[i+1:]
	}
	return full
}

// goroutineID returns the current goroutine's numeric id as the analog of
// Python's %(thread)d. It parses the "goroutine N [" prefix of a one-frame
// stack. This is best-effort and cosmetic — the stable correlation key is the
// request id in the attrs, not this column.
func goroutineID() string {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := string(buf[:n])
	const prefix = "goroutine "
	s = strings.TrimPrefix(s, prefix)
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return "0"
}

// --- package-level convenience helpers -------------------------------------
//
// These write through the slog default installed by Init. Before Init runs,
// slog's own default (text to stderr) applies, so early logs are never lost.

// Debug logs at DEBUG. attrs are alternating key, value pairs (slog style).
func Debug(msg string, attrs ...any) { slog.Debug(msg, attrs...) }

// Info logs at INFO.
func Info(msg string, attrs ...any) { slog.Info(msg, attrs...) }

// Warn logs at WARNING.
func Warn(msg string, attrs ...any) { slog.Warn(msg, attrs...) }

// Error logs at ERROR.
func Error(msg string, attrs ...any) { slog.Error(msg, attrs...) }

// Fatal logs at ERROR and exits the process with status 1 — the replacement
// for the startup log.Fatalf calls, keeping a single log format.
func Fatal(msg string, attrs ...any) {
	slog.Error(msg, attrs...)
	os.Exit(1)
}

// Component returns a logger tagged with the given component label, so its
// lines render as "<component>:<func> - …". Use one per package
// (e.g. logger.Component("persona")).
//
// It builds a textHandler directly against os.Stderr and the shared package
// level, so it works regardless of whether Init has run yet — package-level
// `var log = logger.Component("x")` initializers run before main() calls Init,
// and must still honour the level Init later sets (defaultLevel is a shared
// *slog.LevelVar, so a component logger created early tracks it live).
func Component(name string) *slog.Logger {
	h := &textHandler{
		out:       os.Stderr,
		level:     defaultLevel,
		component: name,
	}
	return slog.New(h)
}

// Err is a small helper to attach an error as a "err" attr, keeping call sites
// terse: logger.Warn("crawl failed", logger.Err(err), "website", w).
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("err", "")
	}
	return slog.String("err", err.Error())
}

// KV is a tiny escape hatch for building an attr when the value type is dynamic.
func KV(k string, v any) slog.Attr { return slog.Any(k, v) }

var _ = fmt.Sprintf // reserved for future formatting helpers

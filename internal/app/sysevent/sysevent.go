// Package sysevent owns process-wide operator event emission.
package sysevent

import (
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// DefaultLogFileName is the backward-compatible operator log file name.
const DefaultLogFileName = "trellooperator.log"

// Event is one gateway-emitted operator log entry.
type Event struct {
	Token   string
	Fields  map[string]any
	Message string
}

// Sink is the callback surface the rest of the codebase emits through.
type Sink interface {
	Emit(Event)
}

var (
	globalMu sync.RWMutex
	global   Sink = discardSink{}
)

// Default returns the process-global sink.
func Default() Sink {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Set replaces the process-global sink. Passing nil restores a discard sink.
func Set(s Sink) {
	if s == nil {
		s = discardSink{}
	}
	globalMu.Lock()
	global = s
	globalMu.Unlock()
}

// Emit emits token without additional fields or suffix text.
func Emit(s Sink, token string) {
	if s == nil {
		s = Default()
	}
	s.Emit(Event{Token: token})
}

// Emitf emits token with a printf-formatted suffix. An empty token writes the
// suffix as-is, for the few legacy operator log lines that are not event=...
// structured.
func Emitf(s Sink, token, format string, args ...any) {
	if s == nil {
		s = Default()
	}
	s.Emit(Event{Token: token, Message: fmt.Sprintf(format, args...)})
}

type discardSink struct{}

func (discardSink) Emit(Event) {}

type loggerSink struct {
	logger *log.Logger
}

// FromLogger adapts a legacy *log.Logger to Sink.
func FromLogger(logger *log.Logger) Sink {
	if logger == nil {
		return Default()
	}
	return loggerSink{logger: logger}
}

func (s loggerSink) Emit(e Event) {
	s.logger.Print(Format(e))
}

// FileSink writes event lines to the default operator log destination.
type FileSink struct {
	logger  *log.Logger
	out     io.Writer
	closer  io.Closer
	logFile string
}

// FileSinkOption tunes NewFileSink.
type FileSinkOption func(*fileSinkConfig)

type fileSinkConfig struct {
	logFile string
	stderr  io.Writer
}

// WithLogFile overrides the log file path.
func WithLogFile(path string) FileSinkOption {
	return func(c *fileSinkConfig) {
		if path != "" {
			c.logFile = path
		}
	}
}

// WithStderr overrides the fallback/warning destination. It is primarily for
// tests; production callers leave it unset.
func WithStderr(w io.Writer) FileSinkOption {
	return func(c *fileSinkConfig) {
		if w != nil {
			c.stderr = w
		}
	}
}

// NewFileSink constructs the default file + stderr-fallback sink.
func NewFileSink(opts ...FileSinkOption) *FileSink {
	cfg := fileSinkConfig{
		logFile: DefaultLogFileName,
		stderr:  os.Stderr,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	out := cfg.stderr
	var closer io.Closer
	// Mode 0o600: the log can capture card ids, model output, prompt
	// previews and timing data. Restrict it to the process owner so a
	// shared host user cannot read it without explicit privilege escalation.
	if f, err := os.OpenFile(cfg.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
		log.New(cfg.stderr, "", log.LstdFlags).Printf("warning: cannot open log file %q (%v); falling back to stderr", cfg.logFile, err)
	} else {
		out = f
		closer = f
	}

	return &FileSink{
		logger:  log.New(out, "", log.LstdFlags),
		out:     out,
		closer:  closer,
		logFile: cfg.logFile,
	}
}

// Emit writes one formatted event line.
func (s *FileSink) Emit(e Event) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Print(Format(e))
}

// Writer returns the destination shared by operator events and gin logs.
func (s *FileSink) Writer() io.Writer {
	if s == nil || s.out == nil {
		return io.Discard
	}
	return s.out
}

// LogFile returns the configured log file path.
func (s *FileSink) LogFile() string {
	if s == nil || s.logFile == "" {
		return DefaultLogFileName
	}
	return s.logFile
}

// Close closes the underlying log file, if one was opened.
func (s *FileSink) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

// Format preserves the existing single-line "event=token k=v ..." shape.
func Format(e Event) string {
	var b strings.Builder
	if e.Token != "" {
		b.WriteString("event=")
		b.WriteString(e.Token)
	}
	if len(e.Fields) > 0 {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%s=%v", k, e.Fields[k])
		}
	}
	if e.Message != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(e.Message)
	}
	return b.String()
}

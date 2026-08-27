// SPDX-License-Identifier: GPL-3.0-or-later

// Package logs is the single logging surface of the backend.
//
// The point of this server is to see exactly what the game sent, what it got
// back, and what it expected when it broke. Every event is therefore one line
// of sorted key=value pairs written to stdout and, when a directory is
// configured, to a dated file. Bodies are captured verbatim when they are
// printable and hex-dumped when they are not, so an encrypted or binary
// payload is still inspectable.
package logs

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const defaultMaxBody = 8192

// Logger writes line-oriented diagnostics. It is safe for concurrent use.
type Logger struct {
	mu          sync.Mutex
	file        *os.File
	traceBodies bool
	maxBody     int
}

// New opens a logger. An empty dir logs to stdout only.
func New(dir string, traceBodies bool, maxBody int) (*Logger, error) {
	l := &Logger{traceBodies: traceBodies, maxBody: maxBody}
	if l.maxBody <= 0 {
		l.maxBody = defaultMaxBody
	}
	if dir == "" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logs: mkdir %s: %w", dir, err)
	}
	name := filepath.Join(dir, fmt.Sprintf("backend-%s.log", time.Now().UTC().Format("2006-01-02")))
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logs: open %s: %w", name, err)
	}
	l.file = file
	return l, nil
}

// Printf writes a free-form line.
func (l *Logger) Printf(format string, args ...any) {
	l.write(stamp() + " " + fmt.Sprintf(format, args...))
}

// Event writes a structured line: "<ts> <kind> k=v k=v".
func (l *Logger) Event(kind string, fields map[string]any) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var line strings.Builder
	line.WriteString(stamp())
	line.WriteByte(' ')
	line.WriteString(kind)
	for _, key := range keys {
		line.WriteByte(' ')
		line.WriteString(key)
		line.WriteByte('=')
		line.WriteString(field(fields[key]))
	}
	l.write(line.String())
}

// Body renders a request or response body for a log line.
func (l *Logger) Body(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if !l.traceBodies {
		return fmt.Sprintf("<%d bytes; body trace disabled>", len(raw))
	}
	shown, suffix := raw, ""
	if len(shown) > l.maxBody {
		shown = shown[:l.maxBody]
		suffix = fmt.Sprintf("...(+%d bytes)", len(raw)-l.maxBody)
	}
	if utf8.Valid(shown) && printable(shown) {
		return string(shown) + suffix
	}
	return "hex:" + hex.EncodeToString(shown) + suffix
}

// Close releases the log file, if any.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Logger) write(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(os.Stdout, line)
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
}

func stamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func field(value any) string {
	text := fmt.Sprint(value)
	if text == "" {
		return `""`
	}
	if strings.ContainsAny(text, " \t\r\n\"") {
		return strconv.Quote(text)
	}
	return text
}

func printable(raw []byte) bool {
	for _, b := range raw {
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			return false
		}
	}
	return true
}

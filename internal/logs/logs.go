// SPDX-License-Identifier: GPL-3.0-or-later

// Package logs is the single logging surface of the backend.
//
// The point of this server is to see exactly what the game sent, what it got
// back, and what it expected when it broke. Every event is therefore one line
// of sorted key=value pairs written to stdout and, when a directory is
// configured, to a dated file. Bodies are captured verbatim when they are
// printable and hex-dumped when they are not, so an encrypted or binary
// payload is still inspectable.
//
// The same lines are also fanned out to live subscribers, which is what the
// admin console's log view reads. Fanout never blocks: a subscriber that stops
// reading loses lines rather than stalling the server that produced them.
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

const (
	defaultMaxBody = 8192

	// maxRecent is how many lines are kept for a console that has just been
	// opened, so a live view starts with context instead of an empty pane.
	maxRecent = 500

	// subscriberQueue is how far one live subscriber may fall behind before it
	// starts losing lines.
	subscriberQueue = 256
)

// Entry is one recorded line, as handed to a live subscriber.
type Entry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Line string    `json:"line"`
}

// Logger writes line-oriented diagnostics. It is safe for concurrent use.
type Logger struct {
	mu          sync.Mutex
	file        *os.File
	traceBodies bool
	maxBody     int

	// recent is a ring of the last maxRecent entries.
	recent []Entry

	subscribers map[uint64]chan Entry
	nextID      uint64
	dropped     uint64
}

// New opens a logger. An empty dir logs to stdout only.
func New(dir string, traceBodies bool, maxBody int) (*Logger, error) {
	l := &Logger{
		traceBodies: traceBodies,
		maxBody:     maxBody,
		subscribers: map[uint64]chan Entry{},
	}
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
	l.emit("", stamp()+" "+fmt.Sprintf(format, args...))
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
	l.emit(kind, line.String())
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

// Recent returns up to limit of the most recent lines, oldest first. A limit of
// zero or less returns everything held.
func (l *Logger) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.recent) {
		limit = len(l.recent)
	}
	out := make([]Entry, limit)
	copy(out, l.recent[len(l.recent)-limit:])
	return out
}

// Subscribe registers a live consumer and returns its id and channel. The
// caller must Unsubscribe, which is what closes the channel.
func (l *Logger) Subscribe() (uint64, <-chan Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	id := l.nextID
	ch := make(chan Entry, subscriberQueue)
	l.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a live consumer and closes its channel.
func (l *Logger) Unsubscribe(id uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.subscribers[id]
	if !ok {
		return
	}
	delete(l.subscribers, id)
	close(ch)
}

// Dropped reports how many lines were lost to slow subscribers. A non-zero
// value means a live view is incomplete, which is worth showing rather than
// hiding.
func (l *Logger) Dropped() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// Close releases the log file, if any, and disconnects every live subscriber.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, ch := range l.subscribers {
		delete(l.subscribers, id)
		close(ch)
	}
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// emit writes one line everywhere it has to go.
func (l *Logger) emit(kind, line string) {
	entry := Entry{At: time.Now().UTC(), Kind: kind, Line: line}

	l.mu.Lock()
	defer l.mu.Unlock()

	fmt.Fprintln(os.Stdout, line)
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}

	l.recent = append(l.recent, entry)
	if len(l.recent) > maxRecent {
		// Re-slice from a copy rather than holding the whole history alive
		// through the underlying array.
		trimmed := make([]Entry, maxRecent)
		copy(trimmed, l.recent[len(l.recent)-maxRecent:])
		l.recent = trimmed
	}

	for _, ch := range l.subscribers {
		select {
		case ch <- entry:
		default:
			// A console that stopped reading must never be able to stall the
			// server whose behaviour it is watching.
			l.dropped++
		}
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

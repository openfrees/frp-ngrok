package hotkey

import (
	"strings"
	"sync"
	"time"
)

const (
	runStatusMaxLines   = 200
	runStatusFlushEvery = 80 * time.Millisecond
)

type runStatusScreen struct {
	maxLines  int
	lines     []string
	current   []byte
	esc       []byte
	pendingCR bool
}

func newRunStatusScreen(maxLines int) *runStatusScreen {
	if maxLines <= 0 {
		maxLines = runStatusMaxLines
	}
	return &runStatusScreen{maxLines: maxLines}
}

func (s *runStatusScreen) feed(p []byte) {
	if len(s.esc) > 0 {
		p = append(append([]byte(nil), s.esc...), p...)
		s.esc = s.esc[:0]
	}
	i := 0
	if s.pendingCR {
		if len(p) == 0 {
			return
		}
		s.pendingCR = false
		if p[0] == '\n' {
			s.commitLine()
			i = 1
		} else {
			s.current = s.current[:0]
		}
	}
	for i < len(p) {
		if p[i] == 0x1b {
			n, ok := s.consumeESC(p[i:])
			if !ok {
				s.esc = append(s.esc[:0], p[i:]...)
				return
			}
			i += n
			continue
		}
		switch p[i] {
		case '\r':
			if i+1 < len(p) && p[i+1] == '\n' {
				s.commitLine()
				i += 2
				continue
			}
			if i+1 >= len(p) {
				s.pendingCR = true
				return
			}
			s.current = s.current[:0]
		case '\n':
			s.commitLine()
		case '\b':
			if len(s.current) > 0 {
				s.current = s.current[:len(s.current)-1]
			}
		default:
			s.current = append(s.current, p[i])
		}
		i++
	}
}

func (s *runStatusScreen) consumeESC(p []byte) (int, bool) {
	if len(p) < 2 {
		return 0, false
	}
	if p[1] != '[' {
		return 2, true
	}
	j := 2
	for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
		j++
	}
	if j >= len(p) {
		return 0, false
	}
	s.applyCSI(p[:j+1])
	return j + 1, true
}

func (s *runStatusScreen) applyCSI(seq []byte) {
	final := seq[len(seq)-1]
	switch final {
	case 'm':
		s.current = append(s.current, seq...)
	case 'K':
		s.current = s.current[:0]
	case 'J':
		s.lines = nil
		s.current = s.current[:0]
	case 'A':
		n := csiCount(seq, 1)
		s.current = s.current[:0]
		for n > 0 && len(s.lines) > 0 {
			s.lines = s.lines[:len(s.lines)-1]
			n--
		}
	}
}

func csiCount(seq []byte, def int) int {
	if len(seq) < 3 {
		return def
	}
	n := 0
	for _, b := range seq[2 : len(seq)-1] {
		if b < '0' || b > '9' {
			break
		}
		n = n*10 + int(b-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

func (s *runStatusScreen) commitLine() {
	s.lines = append(s.lines, string(s.current))
	s.current = s.current[:0]
	if extra := len(s.lines) - s.maxLines; extra > 0 {
		s.lines = append([]string(nil), s.lines[extra:]...)
	}
}

func (s *runStatusScreen) snapshot() string {
	var b strings.Builder
	for i, line := range s.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if len(s.current) == 0 {
		return b.String()
	}
	if len(s.lines) > 0 {
		b.WriteByte('\n')
	}
	b.Write(s.current)
	return b.String()
}

type runStatusFeed struct {
	mu       sync.Mutex
	screen   *runStatusScreen
	emit     func(string)
	closed   bool
	dirty    bool
	timer    *time.Timer
	interval time.Duration
}

func newRunStatusFeed(emit func(string)) *runStatusFeed {
	return &runStatusFeed{
		screen: newRunStatusScreen(runStatusMaxLines),
		emit:   emit,
	}
}

func (f *runStatusFeed) SetFlushInterval(d time.Duration) {
	f.mu.Lock()
	f.interval = d
	f.mu.Unlock()
}

func (f *runStatusFeed) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return len(p), nil
	}
	f.screen.feed(p)
	f.dirty = true
	if f.interval > 0 && f.timer == nil {
		f.timer = time.AfterFunc(f.interval, f.Flush)
	}
	return len(p), nil
}

func (f *runStatusFeed) Flush() {
	f.mu.Lock()
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	if f.closed || !f.dirty || f.emit == nil {
		f.mu.Unlock()
		return
	}
	text := f.screen.snapshot()
	emit := f.emit
	f.dirty = false
	f.mu.Unlock()
	if text != "" {
		emit(text)
	}
}

func (f *runStatusFeed) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.dirty = false
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
}

var runStatusFeeds sync.Map
var runStatusUserClosed sync.Map

func attachRunStatusFeed(id int) *runStatusFeed {
	if id == 0 {
		return nil
	}
	feed := newRunStatusFeed(func(text string) {
		replaceRunStatusOutput(id, text)
	})
	feed.SetFlushInterval(runStatusFlushEvery)
	runStatusFeeds.Store(id, feed)
	return feed
}

func flushRunStatusFeed(id int) {
	if v, ok := runStatusFeeds.Load(id); ok {
		v.(*runStatusFeed).Flush()
	}
}

func closeRunStatusFeed(id int) {
	if v, ok := runStatusFeeds.LoadAndDelete(id); ok {
		v.(*runStatusFeed).Close()
	}
}

func markRunStatusUserClosed(id int) {
	if id == 0 {
		return
	}
	if _, ok := runStatusFeeds.Load(id); !ok {
		return
	}
	runStatusUserClosed.Store(id, true)
	closeRunStatusFeed(id)
}

func takeRunStatusUserClosed(id int) bool {
	_, ok := runStatusUserClosed.LoadAndDelete(id)
	return ok
}

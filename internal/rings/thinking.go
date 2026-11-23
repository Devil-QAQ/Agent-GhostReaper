package rings

import (
	"strings"
	"time"
)

// ThinkingWriter batches actual returned thinking deltas for conversation
// records. Records contain additions, never repeated cumulative bodies. It has
// no goroutine or independent state; callers flush on text, failure and exit.
type ThinkingWriter struct {
	emit    func(string, bool) error
	pending strings.Builder
	active  bool
	last    time.Time
}

func NewThinkingWriter(emit func(string, bool) error) *ThinkingWriter {
	return &ThinkingWriter{emit: emit}
}

func (w *ThinkingWriter) Write(text string) error {
	w.pending.WriteString(text)
	if w.last.IsZero() || w.pending.Len() >= 2048 || time.Since(w.last) >= 200*time.Millisecond {
		return w.Flush()
	}
	return nil
}

func (w *ThinkingWriter) Flush() error {
	if w.pending.Len() == 0 {
		return nil
	}
	if err := w.emit(w.pending.String(), false); err != nil {
		return err
	}
	w.pending.Reset()
	w.active = true
	w.last = time.Now()
	return nil
}

// Finish marks the end of a real thinking segment, without creating a card for
// providers that returned none. A later model/tool round may start another.
func (w *ThinkingWriter) Finish() error {
	if err := w.Flush(); err != nil {
		return err
	}
	if !w.active {
		return nil
	}
	if err := w.emit("", true); err != nil {
		return err
	}
	w.active = false
	return nil
}

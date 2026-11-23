package rings

import (
	"errors"
	"strings"
	"testing"
)

func TestThinkingWriterPreservesDeltasAndBoundedRecords(t *testing.T) {
	var body strings.Builder
	calls, ends := 0, 0
	fail := false
	w := NewThinkingWriter(func(text string, done bool) error {
		if fail {
			return errors.New("ledger unavailable")
		}
		calls++
		body.WriteString(text)
		if done {
			ends++
		}
		return nil
	})
	if err := w.Finish(); err != nil || calls != 0 {
		t.Fatal("no-thinking creates a record")
	}
	want := strings.Repeat("返回内容 ", 1200)
	for _, r := range want {
		if err := w.Write(string(r)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if body.String() != want || ends != 1 || calls > 20 {
		t.Fatalf("lost/duplicated text or unbounded writes: calls=%d ends=%d", calls, ends)
	}
	if err := w.Finish(); err != nil || ends != 1 {
		t.Fatal("duplicate completion")
	}
	fail = true
	if err := w.Write("保留未写片段"); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err == nil {
		t.Fatal("failed ledger reported successful completion")
	}
	fail = false
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if body.String() != want+"保留未写片段" || ends != 2 {
		t.Fatal("failed delta lost or duplicated on recovery")
	}
}

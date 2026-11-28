package capability

import (
	"io"
	"time"
)

// Report at a bounded rate, independent of network packet and file counts.
type progressReader struct {
	reader io.Reader
	report func(int64)
	bytes  int64
	last   time.Time
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	if err != nil || time.Since(r.last) >= 100*time.Millisecond {
		r.report(r.bytes)
		r.last = time.Now()
	}
	return n, err
}

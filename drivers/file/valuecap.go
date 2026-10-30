package file

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// errValueTooLarge reports that one value in a dump is over the size limit. The
// readers wrap it with the format and the record number.
var errValueTooLarge = errors.New("value is over the size limit")

// capped is a dump source with the largest size that one value may have. The
// mongoexport, APOC, and cqlsh CSV readers decode a whole value in memory. So
// the limit stops a crafted dump from making them load a huge value.
type capped struct {
	r     io.Reader
	limit int64
}

// capReader passes reads through to src. It refuses to read more than limit
// bytes past the point that the decoder has consumed. The decoder reads ahead
// of the value that it decodes, so a limit on the whole stream would cap the
// file. This limit is on the bytes of one value, plus the separator before it.
type capReader struct {
	src      io.Reader
	limit    int64
	read     int64
	consumed func() int64
	tripped  bool
}

// Read reads at most the bytes that remain below the limit. It fails with
// errValueTooLarge when no byte remains.
func (c *capReader) Read(p []byte) (int, error) {
	room := c.consumed() + c.limit - c.read
	if room <= 0 {
		c.tripped = true
		return 0, errValueTooLarge
	}
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.src.Read(p)
	c.read += int64(n)
	return n, err
}

// jsonStream reads the values of a JSON dump, JSON Lines or one array, with the
// limit on each value.
type jsonStream struct {
	jd     *json.Decoder
	cap    *capReader
	array  bool
	format string
	limit  int64
	n      int
}

// openJSONStream starts a stream on src. It reports ok=false for an empty
// dump. The format names the dump in errors. The arrayErr text is the context
// of the error for the opening bracket of an array.
func openJSONStream(src capped, format, arrayErr string) (st *jsonStream, ok bool, err error) {
	br := bufio.NewReader(src.r)
	array, err := startsArray(br)
	if errors.Is(err, io.EOF) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", format, err)
	}
	cr := &capReader{src: br, limit: src.limit}
	jd := json.NewDecoder(cr)
	cr.consumed = jd.InputOffset
	if array {
		if _, err := jd.Token(); err != nil { // consume '['.
			return nil, false, fmt.Errorf("%s: %w", arrayErr, err)
		}
	}
	return &jsonStream{jd: jd, cap: cr, array: array, format: format, limit: src.limit}, true, nil
}

// next decodes the next value into v. It reports false at the clean end of the
// dump. A value over the limit is an error.
func (s *jsonStream) next(v any) (bool, error) {
	s.n++
	if s.array && !s.jd.More() {
		if s.cap.tripped {
			return false, tooLarge(s.format, s.n, s.limit)
		}
		return false, nil
	}
	err := s.jd.Decode(v)
	if errors.Is(err, errValueTooLarge) {
		return false, tooLarge(s.format, s.n, s.limit)
	}
	if err == io.EOF { //nolint:errorlint // the decoder returns a bare io.EOF only at a clean end between values, never inside an array. A wrapped one is a reader failure.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("decode %s: %w", s.format, err)
	}
	return true, nil
}

// csvRows reads cqlsh CSV records, each within the limit.
type csvRows struct {
	*csv.Reader
	cap   *capReader
	start int64
}

// csvReader returns a CSV reader whose single record cannot grow past the limit.
func (c capped) csvReader() *csvRows {
	rows := &csvRows{cap: &capReader{src: c.r, limit: c.limit}}
	rows.cap.consumed = func() int64 { return rows.start }
	rows.Reader = csv.NewReader(rows.cap)
	return rows
}

// Read returns the next record. The limit for the record after it starts at the
// end of this one, because a quoted field moves the offset inside a record.
func (r *csvRows) Read() ([]string, error) {
	fields, err := r.Reader.Read()
	r.start = r.InputOffset()
	return fields, err
}

// tooLarge builds the error for a value over the limit. The text names the
// format, the record number, and the limit. It never holds the value.
func tooLarge(format string, record int, limit int64) error {
	return fmt.Errorf("%s record %d: %w (%d bytes)", format, record, errValueTooLarge, limit)
}

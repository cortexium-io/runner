package subprocess

import "bytes"

type boundedCapture struct {
	buffer    bytes.Buffer
	limit     int
	marker    string
	truncated bool
}

func newBoundedCapture(limit int, marker string) *boundedCapture {
	if limit < 0 {
		limit = 0
	}
	if len(marker) > limit {
		marker = marker[:limit]
	}
	return &boundedCapture{limit: limit, marker: marker}
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	written := len(data)
	remaining := capture.limit - capture.buffer.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = capture.buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		capture.truncated = true
	}
	return written, nil
}

func (capture *boundedCapture) String() string {
	if !capture.truncated {
		return capture.buffer.String()
	}
	dataBytes := capture.limit - len(capture.marker)
	return capture.buffer.String()[:dataBytes] + capture.marker
}

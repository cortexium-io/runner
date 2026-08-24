package subprocess

import "bytes"

type headTailCapture struct {
	buffer    bytes.Buffer
	head      []byte
	tail      []byte
	limit     int
	headLimit int
	tailLimit int
	marker    string
	truncated bool
}

func newHeadTailCapture(limit int, marker string) *headTailCapture {
	if limit < 0 {
		limit = 0
	}
	if len(marker) > limit {
		marker = marker[:limit]
	}
	contentLimit := limit - len(marker)
	headLimit := contentLimit / 2
	return &headTailCapture{limit: limit, headLimit: headLimit, tailLimit: contentLimit - headLimit, marker: marker}
}

func (capture *headTailCapture) Write(data []byte) (int, error) {
	written := len(data)
	if !capture.truncated {
		if capture.buffer.Len()+len(data) <= capture.limit {
			_, _ = capture.buffer.Write(data)
			return written, nil
		}
		combined := append(append([]byte(nil), capture.buffer.Bytes()...), data...)
		capture.buffer.Reset()
		capture.truncated = true
		capture.head = append([]byte(nil), combined[:capture.headLimit]...)
		if capture.tailLimit > 0 {
			capture.tail = append([]byte(nil), combined[len(combined)-capture.tailLimit:]...)
		}
		return written, nil
	}
	if capture.tailLimit == 0 {
		return written, nil
	}
	capture.tail = append(capture.tail, data...)
	if len(capture.tail) > capture.tailLimit {
		offset := len(capture.tail) - capture.tailLimit
		copy(capture.tail, capture.tail[offset:])
		capture.tail = capture.tail[:capture.tailLimit]
	}
	return written, nil
}

func (capture *headTailCapture) String() string {
	if !capture.truncated {
		return capture.buffer.String()
	}
	return string(capture.head) + capture.marker + string(capture.tail)
}

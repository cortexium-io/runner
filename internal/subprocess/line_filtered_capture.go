package subprocess

import "bytes"

type lineFilteredCapture struct {
	pending    []byte
	output     *boundedCapture
	keep       LineFilter
	maxLine    int
	discarding bool
}

func newLineFilteredCapture(limit int, marker string, keep LineFilter) *lineFilteredCapture {
	if limit < 0 {
		limit = 0
	}
	return &lineFilteredCapture{output: newBoundedCapture(limit, marker), keep: keep, maxLine: limit}
}

func (capture *lineFilteredCapture) Write(data []byte) (int, error) {
	written := len(data)
	if capture.discarding {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			return written, nil
		}
		capture.discarding = false
		data = data[index+1:]
	}
	capture.pending = append(capture.pending, data...)
	for {
		index := bytes.IndexByte(capture.pending, '\n')
		if index < 0 {
			break
		}
		if index <= capture.maxLine {
			capture.writeLine(capture.pending[:index])
		}
		capture.pending = append(capture.pending[:0], capture.pending[index+1:]...)
	}
	if len(capture.pending) > capture.maxLine {
		capture.pending = nil
		capture.discarding = true
	}
	return written, nil
}

func (capture *lineFilteredCapture) Flush() {
	if len(capture.pending) == 0 || capture.discarding {
		return
	}
	capture.writeLine(capture.pending)
	capture.pending = nil
}

func (capture *lineFilteredCapture) String() string { return capture.output.String() }

func (capture *lineFilteredCapture) writeLine(line []byte) {
	if capture.keep != nil && !capture.keep(line) {
		return
	}
	_, _ = capture.output.Write(line)
	_, _ = capture.output.Write([]byte{'\n'})
}

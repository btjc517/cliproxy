package helps

import (
	"bytes"
	"io"
)

// ObserveSSEData wraps an SSE body so observe sees each data payload as soon
// as its line is read. Executors that read a streamed reply to the end before
// parsing it can still time its events. Once observe returns false the wrapper
// stops looking and only passes bytes through.
func ObserveSSEData(body io.Reader, observe func(payload []byte) bool) io.Reader {
	return &sseDataObserver{body: body, observe: observe}
}

type sseDataObserver struct {
	body    io.Reader
	observe func(payload []byte) bool
	line    []byte
	done    bool
}

func (o *sseDataObserver) Read(p []byte) (int, error) {
	n, err := o.body.Read(p)
	chunk := p[:n]
	for !o.done && len(chunk) > 0 {
		end := bytes.IndexByte(chunk, '\n')
		if end < 0 {
			o.line = append(o.line, chunk...)
			break
		}
		o.line = append(o.line, chunk[:end]...)
		chunk = chunk[end+1:]
		o.emit()
	}
	if err != nil && !o.done && len(o.line) > 0 {
		o.emit()
	}
	return n, err
}

// emit hands the buffered line to observe when it carries data.
func (o *sseDataObserver) emit() {
	line := bytes.TrimRight(o.line, "\r")
	if payload, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		if payload = bytes.TrimSpace(payload); len(payload) > 0 && !o.observe(payload) {
			o.done = true
		}
	}
	o.line = o.line[:0]
	if o.done {
		o.line = nil
	}
}

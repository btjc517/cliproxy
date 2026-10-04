package helps

import (
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// chunkReader returns one chunk per Read.
type chunkReader struct{ chunks []string }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if r.chunks[0] == "" {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

// Each data payload reaches observe in the Read that completes its line, not
// after the whole body, and the bytes pass through unchanged.
func TestObserveSSEDataSeesEventsAsTheyArrive(t *testing.T) {
	source := &chunkReader{chunks: []string{
		"event: x\ndata: {\"type\":\"response.created\"}\n\nda",
		"ta: {\"type\":\"response.output_text.delta\"}\r\n\n",
		"data: tail",
	}}
	var seen []string
	reader := ObserveSSEData(source, func(payload []byte) bool {
		seen = append(seen, string(payload))
		return true
	})
	buf := make([]byte, 256)
	var body []byte
	wantAfter := [][]string{
		{`{"type":"response.created"}`},
		{`{"type":"response.created"}`, `{"type":"response.output_text.delta"}`},
		{`{"type":"response.created"}`, `{"type":"response.output_text.delta"}`},
	}
	for i, want := range wantAfter {
		n, err := reader.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		body = append(body, buf[:n]...)
		if len(seen) != len(want) {
			t.Fatalf("after read %d saw %q, want %q", i, seen, want)
		}
		for j := range want {
			if seen[j] != want[j] {
				t.Fatalf("after read %d saw %q, want %q", i, seen, want)
			}
		}
	}
	if _, err := reader.Read(buf); err != io.EOF {
		t.Fatalf("final read error = %v, want EOF", err)
	}
	if len(seen) != 3 || seen[2] != "tail" {
		t.Fatalf("saw %q, want the unterminated last line at EOF", seen)
	}
	if want := "event: x\ndata: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_text.delta\"}\r\n\ndata: tail"; string(body) != want {
		t.Fatalf("body = %q, want it unchanged", body)
	}
}

func TestObserveSSEDataStopsWhenAsked(t *testing.T) {
	source := &chunkReader{chunks: []string{"data: 1\ndata: 2\n", "data: 3\n"}}
	calls := 0
	data, err := io.ReadAll(ObserveSSEData(source, func([]byte) bool {
		calls++
		return false
	}))
	if err != nil || string(data) != "data: 1\ndata: 2\ndata: 3\n" || calls != 1 {
		t.Fatalf("data %q err %v calls %d, want all bytes and one call", data, err, calls)
	}
}

// The reporter's upstream stream flag starts from the client's stream flag
// and an executor can override it.
func TestUsageReporterUpstreamStream(t *testing.T) {
	reporter := NewUsageReporter(usage.WithStream(context.Background(), false), "codex", "gpt-5.5", nil)
	if record := reporter.buildRecordForModel("gpt-5.5", usage.Detail{}, false, usage.Failure{}); record.UpstreamStream || record.Stream {
		t.Fatalf("default record upstream %v stream %v, want both false", record.UpstreamStream, record.Stream)
	}
	reporter.SetUpstreamStream(true)
	if record := reporter.buildRecordForModel("gpt-5.5", usage.Detail{}, false, usage.Failure{}); !record.UpstreamStream || record.Stream {
		t.Fatalf("record upstream %v stream %v, want upstream only", record.UpstreamStream, record.Stream)
	}
	streamed := NewUsageReporter(usage.WithStream(context.Background(), true), "codex", "gpt-5.5", nil)
	if record := streamed.buildRecordForModel("gpt-5.5", usage.Detail{}, false, usage.Failure{}); !record.UpstreamStream || !record.Stream {
		t.Fatalf("streamed record upstream %v stream %v, want both true", record.UpstreamStream, record.Stream)
	}
}

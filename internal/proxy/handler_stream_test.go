package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/watchword/watchword/internal/domain"
)

// countingWriter is an http.ResponseWriter that discards the body but counts
// bytes written. It optionally fails after failAfter bytes (simulating a
// client that disconnects mid-download).
type countingWriter struct {
	header    http.Header
	status    int
	written   int64
	failAfter int64 // 0 = never fail
}

func newCountingWriter() *countingWriter { return &countingWriter{header: http.Header{}} }

func (w *countingWriter) Header() http.Header { return w.header }
func (w *countingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *countingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.failAfter > 0 && w.written+int64(len(p)) > w.failAfter {
		return 0, errors.New("client went away")
	}
	w.written += int64(len(p))
	return len(p), nil
}

// guardedReader yields size bytes but fails if the handler pulls more than
// maxAhead bytes from it that have not yet been written to the client. That
// would mean the handler is buffering the object instead of streaming it.
type guardedReader struct {
	size     int64
	read     int64
	maxAhead int64
	out      *countingWriter
	closed   bool
}

func (g *guardedReader) Read(p []byte) (int, error) {
	if g.read >= g.size {
		return 0, io.EOF
	}
	if ahead := g.read - g.out.written; ahead > g.maxAhead {
		return 0, fmt.Errorf("handler buffered %d unwritten bytes (limit %d)", ahead, g.maxAhead)
	}
	n := int64(len(p))
	if rem := g.size - g.read; n > rem {
		n = rem
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	g.read += n
	return int(n), nil
}

func (g *guardedReader) Close() error { g.closed = true; return nil }

type readerStreamer struct {
	body        io.ReadCloser
	contentType string
	size        int64
}

func (s *readerStreamer) GetObject(_ context.Context, _ string) (io.ReadCloser, string, int64, error) {
	return s.body, s.contentType, s.size, nil
}

func (s *readerStreamer) PutObject(_ context.Context, _ string, _ io.Reader, _ string, _ int64) error {
	return nil
}

// recordingRepo captures RecordDownload calls. The call blocks until release
// is closed so the test can cancel the request context first.
type recordingRepo struct {
	mockRepo
	release chan struct{}
	calls   chan recordCall
}

type recordCall struct {
	ctxErr      error
	hasDeadline bool
	clientIP    string
	userAgent   string
}

func (r *recordingRepo) RecordDownload(ctx context.Context, _ uuid.UUID, _, _, clientIP, userAgent string) error {
	<-r.release
	_, hasDeadline := ctx.Deadline()
	r.calls <- recordCall{ctxErr: ctx.Err(), hasDeadline: hasDeadline, clientIP: clientIP, userAgent: userAgent}
	return ctx.Err()
}

func fileEntry(id uuid.UUID, filename string) *domain.Entry {
	return &domain.Entry{
		ID:        id,
		Word:      "bigfile",
		EntryType: domain.EntryTypeFile,
		Payload:   `{"s3_key":"` + id.String() + `/` + filename + `","filename":"` + filename + `","content_type":"application/zip","size_limit":1073741824}`,
	}
}

func TestHandler_StreamsWithoutBuffering(t *testing.T) {
	const size = 64 << 20    // 64 MiB, half the production memory limit
	const maxAhead = 1 << 20 // never more than 1 MiB read-but-unwritten

	secret := "test-secret"
	entryID := uuid.New()
	repo := &recordingRepo{
		mockRepo: mockRepo{entry: fileEntry(entryID, "guide.zip")},
		release:  make(chan struct{}),
		calls:    make(chan recordCall, 1),
	}
	close(repo.release)

	w := newCountingWriter()
	body := &guardedReader{size: size, maxAhead: maxAhead, out: w}
	handler := NewHandler(secret, &readerStreamer{body: body, contentType: "application/zip", size: size}, repo, slog.Default())

	signedURL := SignDownloadURL("http://localhost", secret, entryID.String(), "guide.zip", 5*time.Minute)
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL, nil))

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if w.written != size {
		t.Fatalf("written = %d, want %d (stream aborted: handler likely buffered)", w.written, size)
	}
	if !body.closed {
		t.Error("S3 body was not closed")
	}
	if got := w.header.Get("Content-Length"); got != fmt.Sprint(size) {
		t.Errorf("Content-Length = %q, want %d", got, size)
	}
	if got := w.header.Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := w.header.Get("Content-Disposition"); got != `attachment; filename="guide.zip"` {
		t.Errorf("Content-Disposition = %q", got)
	}

	select {
	case <-repo.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("download was not recorded")
	}
}

func TestHandler_RecordDownloadSurvivesRequestCancel(t *testing.T) {
	secret := "test-secret"
	entryID := uuid.New()
	repo := &recordingRepo{
		mockRepo: mockRepo{entry: fileEntry(entryID, "guide.zip")},
		release:  make(chan struct{}),
		calls:    make(chan recordCall, 1),
	}
	handler := NewHandler(secret, &mockStreamer{body: "zipdata", contentType: "application/zip", size: 7}, repo, slog.Default())

	signedURL := SignDownloadURL("http://localhost", secret, entryID.String(), "guide.zip", 5*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, signedURL, nil).WithContext(ctx)
	req.RemoteAddr = "203.0.113.7:4321"
	req.Header.Set("User-Agent", "curl/8")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	// net/http cancels the request context once the handler returns (or the
	// client disconnects). The background record must not inherit that.
	cancel()
	close(repo.release)

	select {
	case c := <-repo.calls:
		if c.ctxErr != nil {
			t.Errorf("RecordDownload ctx error = %v, want nil", c.ctxErr)
		}
		if !c.hasDeadline {
			t.Error("RecordDownload ctx has no deadline; want a bounded timeout")
		}
		if c.clientIP != "203.0.113.7:4321" || c.userAgent != "curl/8" {
			t.Errorf("client info = %q / %q", c.clientIP, c.userAgent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download was not recorded")
	}
}

func TestHandler_ClientDisconnectNotRecorded(t *testing.T) {
	secret := "test-secret"
	entryID := uuid.New()
	repo := &recordingRepo{
		mockRepo: mockRepo{entry: fileEntry(entryID, "guide.zip")},
		release:  make(chan struct{}),
		calls:    make(chan recordCall, 1),
	}
	close(repo.release)

	w := newCountingWriter()
	w.failAfter = 100 << 10
	body := &guardedReader{size: 8 << 20, maxAhead: 1 << 20, out: w}
	handler := NewHandler(secret, &readerStreamer{body: body, contentType: "application/zip", size: 8 << 20}, repo, slog.Default())

	signedURL := SignDownloadURL("http://localhost", secret, entryID.String(), "guide.zip", 5*time.Minute)
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, signedURL, nil))

	if !body.closed {
		t.Error("S3 body was not closed after client disconnect")
	}
	if body.read >= body.size {
		t.Error("handler kept reading the whole object after the client went away")
	}
	select {
	case <-repo.calls:
		t.Error("incomplete download was recorded")
	case <-time.After(100 * time.Millisecond):
	}
}

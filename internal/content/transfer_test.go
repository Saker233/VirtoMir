package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
)

func TestServeContent_FullDownload(t *testing.T) {
	data := []byte("0123456789ABCDEF0123456789ABCDEF")
	tmp := filepath.Join(t.TempDir(), "test-file.bin")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/download", nil)
	rec := httptest.NewRecorder()

	ServeContent(rec, req, tmp, int64(len(data)))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(data)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(data))
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Errorf("body mismatch: got %q, want %q", rec.Body.String(), string(data))
	}
}

func TestServeContent_RangeRequests(t *testing.T) {
	data := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	total := int64(len(data))
	tmp := filepath.Join(t.TempDir(), "test-file.bin")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	tests := []struct {
		name        string
		rangeHdr    string
		wantStatus  int
		wantRange   string
		wantLen     string
		wantPayload []byte
	}{
		{
			name:        "closed range start to middle",
			rangeHdr:    "bytes=0-9",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 0-9/%d", total),
			wantLen:     "10",
			wantPayload: data[0:10],
		},
		{
			name:        "open ended range from offset to end",
			rangeHdr:    "bytes=10-",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 10-%d/%d", total-1, total),
			wantLen:     strconv.FormatInt(total-10, 10),
			wantPayload: data[10:],
		},
		{
			name:        "middle subrange",
			rangeHdr:    "bytes=5-15",
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes 5-15/%d", total),
			wantLen:     "11",
			wantPayload: data[5:16],
		},
		{
			name:        "single byte range at end",
			rangeHdr:    fmt.Sprintf("bytes=%d-%d", total-1, total-1),
			wantStatus:  http.StatusPartialContent,
			wantRange:   fmt.Sprintf("bytes %d-%d/%d", total-1, total-1, total),
			wantLen:     "1",
			wantPayload: data[total-1 : total],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/download", nil)
			req.Header.Set("Range", tc.rangeHdr)
			rec := httptest.NewRecorder()

			ServeContent(rec, req, tmp, total)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if cr := rec.Header().Get("Content-Range"); cr != tc.wantRange {
				t.Errorf("Content-Range = %q, want %q", cr, tc.wantRange)
			}
			if cl := rec.Header().Get("Content-Length"); cl != tc.wantLen {
				t.Errorf("Content-Length = %q, want %q", cl, tc.wantLen)
			}
			if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
				t.Errorf("Accept-Ranges = %q, want bytes", ar)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.wantPayload) {
				t.Errorf("body = %q, want %q", rec.Body.String(), string(tc.wantPayload))
			}
		})
	}
}

func TestServeContent_OutOfBounds(t *testing.T) {
	data := []byte("0123456789")
	total := int64(len(data))
	tmp := filepath.Join(t.TempDir(), "test-file.bin")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	invalidRanges := []string{
		"bytes=10-20",    // start >= total
		"bytes=20-30",    // start > total
		"bytes=8-5",      // start > end
		"bytes=5-15",     // end >= total
		"bytes=-5",       // omitted start
		"bytes=abc-def",  // non-numeric
		"bytes=0-abc",    // non-numeric end
		"invalid",        // missing bytes= prefix
		"characters=0-5", // unsupported unit
	}

	for _, rHdr := range invalidRanges {
		t.Run(rHdr, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/download", nil)
			req.Header.Set("Range", rHdr)
			rec := httptest.NewRecorder()

			ServeContent(rec, req, tmp, total)

			if rec.Code != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("status = %d, want %d (416)", rec.Code, http.StatusRequestedRangeNotSatisfiable)
			}
			wantCR := fmt.Sprintf("bytes */%d", total)
			if cr := rec.Header().Get("Content-Range"); cr != wantCR {
				t.Errorf("Content-Range = %q, want %q", cr, wantCR)
			}
		})
	}
}

func TestPull_ResumableTransfer(t *testing.T) {
	// 100,000 bytes payload
	payload := make([]byte, 100000)
	for i := range payload {
		payload[i] = byte((i * 37) % 256)
	}
	h := sha256.Sum256(payload)
	payloadSHA := hex.EncodeToString(h[:])

	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "source.qcow2")
	if err := os.WriteFile(srcFile, payload, 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	var reqCount atomic.Int32
	var secondRange atomic.Value

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := reqCount.Add(1)
		if count == 1 {
			// First request: simulate disconnect after exactly 50% bytes
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:50000])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			// Abort connection
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
			}
			return
		}

		// Second request: record received Range header and serve remainder via ServeContent
		secondRange.Store(r.Header.Get("Range"))
		ServeContent(w, r, srcFile, int64(len(payload)))
	}))
	defer ts.Close()

	item := Item{
		ID:   "item-test-resume",
		Name: "debian-cloud",
		Type: TypeImage,
	}
	version := ItemVersion{
		ID:      "v1-resume",
		Version: "12.0",
		Format:  "qcow2",
		Size:    int64(len(payload)),
		SHA256:  payloadSHA,
	}

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	targetDir := t.TempDir()
	tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))
	finalPath := filepath.Join(targetDir, fmt.Sprintf("content-%s-v%s.%s", item.Name, version.Version, version.Format))

	// Step 1: Start initial pull, which gets cut off at 50%
	_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if err == nil {
		t.Fatalf("expected error on interrupted transfer, got nil")
	}

	// Verify partial temp file exists with 50,000 bytes
	st, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatalf("expected partial temp file to exist: %v", err)
	}
	if st.Size() != 50000 {
		t.Fatalf("partial temp file size = %d, want 50000", st.Size())
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final file should not exist yet")
	}

	// Step 2: Start second Pull to resume from offset
	var reportedMsgs []string
	var reportMu sync.Mutex
	report := func(msg string) {
		reportMu.Lock()
		reportedMsgs = append(reportedMsgs, msg)
		reportMu.Unlock()
	}

	gotPath, err := Pull(context.Background(), peer, item, version, targetDir, report)
	if err != nil {
		t.Fatalf("resumed pull failed: %v", err)
	}
	if gotPath != finalPath {
		t.Errorf("got path = %q, want %q", gotPath, finalPath)
	}

	// Assert Range header on second request was sent with 50% offset
	rangeSent, _ := secondRange.Load().(string)
	if rangeSent != "bytes=50000-" {
		t.Errorf("second request Range header = %q, want bytes=50000-", rangeSent)
	}

	// Assert final file exists and content matches original payload completely
	finalBytes, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(finalBytes, payload) {
		t.Fatalf("final file content does not match original payload")
	}

	// Assert temporary file is gone
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file %s still exists", tmpPath)
	}

	// Assert progress report was called
	reportMu.Lock()
	if len(reportedMsgs) == 0 {
		t.Errorf("expected progress reports to be generated")
	}
	reportMu.Unlock()
}

func TestPull_ChecksumMismatch(t *testing.T) {
	// Expected payload
	expectedPayload := []byte("clean-unaltered-template-payload")
	h := sha256.Sum256(expectedPayload)
	expectedSHA := hex.EncodeToString(h[:])

	// Corrupted payload returned by server (same size, different bytes)
	corruptPayload := []byte("corrupted-tampered-payload-data!")
	if len(corruptPayload) != len(expectedPayload) {
		t.Fatalf("test setup: sizes must match")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(corruptPayload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(corruptPayload)
	}))
	defer ts.Close()

	item := Item{
		ID:   "item-corrupt",
		Name: "corrupt-img",
		Type: TypeImage,
	}
	version := ItemVersion{
		ID:      "v1-corrupt",
		Version: "1.0",
		Format:  "iso",
		Size:    int64(len(expectedPayload)),
		SHA256:  expectedSHA,
	}

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	targetDir := t.TempDir()
	tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))
	finalPath := filepath.Join(targetDir, fmt.Sprintf("content-%s-v%s.%s", item.Name, version.Version, version.Format))

	_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if err == nil {
		t.Fatalf("expected ErrChecksumMismatch, got nil")
	}

	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected error to wrap ErrChecksumMismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "checksum mismatch: expected") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Verify temp file was deleted
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("corrupted temp file was not removed: %v", err)
	}

	// Verify final file was not created
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Errorf("final file should not exist on checksum mismatch")
	}
}

func TestPull_EmptyChecksum(t *testing.T) {
	item := Item{ID: "item-empty-sha", Name: "empty-sha-img", Type: TypeImage}
	version := ItemVersion{ID: "v1-empty-sha", Version: "1.0", Format: "iso", Size: 100, SHA256: ""}

	peer := cluster.NewPeer(cluster.Node{Address: "http://127.0.0.1:9"}, nil)
	targetDir := t.TempDir()

	_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if err == nil {
		t.Fatalf("expected error on empty SHA256, got nil")
	}
	if !strings.Contains(err.Error(), "checksum cannot be empty") {
		t.Errorf("error = %q, want containing 'checksum cannot be empty'", err.Error())
	}
}

// TestPull_ChecksumMismatchPreservesExistingFinal verifies that a failed re-download does
// not destroy an already checksum-verified final file sitting in the pool.
func TestPull_ChecksumMismatchPreservesExistingFinal(t *testing.T) {
	good := []byte("good-local-copy-that-is-checksum-verified")
	bad := append([]byte{}, good...)
	bad[0] ^= 0xff
	if len(good) != len(bad) {
		t.Fatalf("test setup: payload sizes must match")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(bad)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bad)
	}))
	defer ts.Close()

	item := Item{ID: "item-preserve", Name: "preserve", Type: TypeImage}
	version := ItemVersion{ID: "v1-preserve", Version: "1.0", Format: "qcow2", Size: int64(len(good)), SHA256: sha256Hex(good)}

	targetDir := t.TempDir()
	finalPath := filepath.Join(targetDir, "content-preserve-v1.0.qcow2")
	if err := os.WriteFile(finalPath, good, 0644); err != nil {
		t.Fatalf("write existing final file: %v", err)
	}

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}

	kept, readErr := os.ReadFile(finalPath)
	if readErr != nil {
		t.Fatalf("existing final file was deleted on a failed re-download: %v", readErr)
	}
	if !bytes.Equal(kept, good) {
		t.Errorf("existing final file was modified: got %q, want %q", kept, good)
	}
}

func TestPull_ServerReturns200OKInsteadOf206(t *testing.T) {
	payload := []byte("entire-new-payload-from-scratch")
	h := sha256.Sum256(payload)
	payloadSHA := hex.EncodeToString(h[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server ignores Range header and returns 200 OK with full content
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	item := Item{
		ID:   "item-ok200",
		Name: "fallback-item",
		Type: TypeTemplate,
	}
	version := ItemVersion{
		ID:      "v1-ok200",
		Version: "2.0",
		Format:  "qcow2",
		Size:    int64(len(payload)),
		SHA256:  payloadSHA,
	}

	targetDir := t.TempDir()
	tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))
	finalPath := filepath.Join(targetDir, fmt.Sprintf("content-%s-v%s.%s", item.Name, version.Version, version.Format))

	// Pre-create partial temp file with garbage
	if err := os.WriteFile(tmpPath, []byte("stale-partial-bytes"), 0644); err != nil {
		t.Fatalf("write stale tmp file: %v", err)
	}

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	gotPath, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if err != nil {
		t.Fatalf("Pull failed: %v", err)
	}
	if gotPath != finalPath {
		t.Errorf("gotPath = %q, want %q", gotPath, finalPath)
	}

	// Verify final file content
	gotBytes, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(gotBytes, payload) {
		t.Errorf("final content = %q, want %q", string(gotBytes), string(payload))
	}
}

func TestPruneLocalFile(t *testing.T) {
	dir := t.TempDir()
	fileName := "content-ubuntu-v22.04.1.qcow2"
	filePath := filepath.Join(dir, fileName)
	if err := os.WriteFile(filePath, []byte("volume data"), 0644); err != nil {
		t.Fatalf("write test volume: %v", err)
	}

	// Prune existing file
	if err := PruneLocalFile(dir, "ubuntu", "22.04.1", "qcow2"); err != nil {
		t.Fatalf("PruneLocalFile failed: %v", err)
	}

	// Verify deleted
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("file %s was not pruned", filePath)
	}

	// Pruning again (non-existent file) should succeed without error
	if err := PruneLocalFile(dir, "ubuntu", "22.04.1", "qcow2"); err != nil {
		t.Fatalf("PruneLocalFile on missing file failed: %v", err)
	}
}

func TestPull_ContextCancellation(t *testing.T) {
	payload := make([]byte, 20000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:5000])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write(payload[5000:])
	}))
	defer ts.Close()

	item := Item{ID: "item-cancel", Name: "cancel-vm", Type: TypeImage}
	version := ItemVersion{ID: "v1-cancel", Version: "1.0", Format: "qcow2", Size: int64(len(payload)), SHA256: "somehash"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	targetDir := t.TempDir()
	tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))

	_, err := Pull(ctx, peer, item, version, targetDir, nil)
	if err == nil {
		t.Fatalf("expected error on context cancel")
	}

	// Partial file should still be preserved
	if _, err := os.Stat(tmpPath); os.IsNotExist(err) {
		t.Errorf("expected partial temp file to remain on disk after cancel")
	}
}

func TestPeer_DownloadHeaders(t *testing.T) {
	var gotAuth, gotRange, gotUser, gotRole, gotToken, gotCustom string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotRange = r.Header.Get("Range")
		gotUser = r.Header.Get("X-VirtoMir-User")
		gotRole = r.Header.Get("X-VirtoMir-Role")
		gotToken = r.Header.Get("X-VirtoMir-Token")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client(), "node-test-1", "secret-test-key").
		As("test-user", "admin", "token-12345")

	headers := map[string]string{
		"Range":    "bytes=100-200",
		"X-Custom": "custom-value",
	}

	resp, err := peer.Download(context.Background(), "/api/v1/test", headers)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	defer resp.Body.Close()

	if wantAuth := cluster.AuthScheme + "node-test-1.secret-test-key"; gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if wantRange := "bytes=100-200"; gotRange != wantRange {
		t.Errorf("Range = %q, want %q", gotRange, wantRange)
	}
	if gotUser != "test-user" {
		t.Errorf("X-VirtoMir-User = %q, want test-user", gotUser)
	}
	if gotRole != "admin" {
		t.Errorf("X-VirtoMir-Role = %q, want admin", gotRole)
	}
	if gotToken != "token-12345" {
		t.Errorf("X-VirtoMir-Token = %q, want token-12345", gotToken)
	}
	if gotCustom != "custom-value" {
		t.Errorf("X-Custom = %q, want custom-value", gotCustom)
	}

	// Test peer without credentials sends no Authorization header
	var unauthAuth string
	tsUnauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unauthAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer tsUnauth.Close()

	unauthPeer := cluster.NewPeer(cluster.Node{Address: tsUnauth.URL}, tsUnauth.Client())
	respUnauth, err := unauthPeer.Download(context.Background(), "/api/v1/test", nil)
	if err != nil {
		t.Fatalf("Download unauth failed: %v", err)
	}
	defer respUnauth.Body.Close()
	if unauthAuth != "" {
		t.Errorf("expected no Authorization header for unauthenticated peer, got %q", unauthAuth)
	}
}

func TestPull_MismatchedOrFaultyContentRange(t *testing.T) {
	totalSize := int64(100000)
	partialOffset := int64(50000)

	cases := []struct {
		name         string
		contentRange string
		setupPartial bool
		statusCode   int
		wantErr      string
	}{
		{
			name:         "mismatched start offset (server returns 0 instead of 50000)",
			contentRange: "bytes 0-99999/100000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "Content-Range start offset mismatch",
		},
		{
			name:         "mismatched start offset (server returns 25000 instead of 50000)",
			contentRange: "bytes 25000-99999/100000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "Content-Range start offset mismatch",
		},
		{
			name:         "missing Content-Range header on 206",
			contentRange: "",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "missing Content-Range header",
		},
		{
			name:         "malformed Content-Range unit",
			contentRange: "characters 50000-99999/100000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "invalid Content-Range unit",
		},
		{
			name:         "malformed range format",
			contentRange: "bytes */100000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "invalid Content-Range byte range",
		},
		{
			name:         "end offset out of bounds",
			contentRange: "bytes 50000-150000/100000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "Content-Range end exceeds total size",
		},
		{
			name:         "total size mismatch",
			contentRange: "bytes 50000-99999/200000",
			setupPartial: true,
			statusCode:   http.StatusPartialContent,
			wantErr:      "Content-Range total size mismatch",
		},
		{
			name:         "206 returned when full download requested",
			contentRange: "bytes 0-99999/100000",
			setupPartial: false,
			statusCode:   http.StatusPartialContent,
			wantErr:      "peer returned 206 Partial Content when full download was requested",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				if tc.contentRange != "" {
					w.Header().Set("Content-Range", tc.contentRange)
				}
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write(bytes.Repeat([]byte("X"), 50000))
			}))
			defer ts.Close()

			item := Item{ID: "item-cr-test", Name: "cr-item", Type: TypeImage}
			version := ItemVersion{ID: "v1-cr", Version: "1.0", Format: "qcow2", Size: totalSize, SHA256: "somehash"}

			targetDir := t.TempDir()
			tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))

			if tc.setupPartial {
				initialData := bytes.Repeat([]byte("A"), int(partialOffset))
				if err := os.WriteFile(tmpPath, initialData, 0644); err != nil {
					t.Fatalf("write initial partial file: %v", err)
				}
			}

			peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client(), "node-1", "key-1")
			_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantErr)
			}

			// Verify partial temp file was NOT corrupted or grown by append
			if tc.setupPartial {
				st, err := os.Stat(tmpPath)
				if err != nil {
					t.Fatalf("expected partial temp file to remain on disk: %v", err)
				}
				if st.Size() != partialOffset {
					t.Fatalf("temp file was modified/corrupted: size = %d, want %d", st.Size(), partialOffset)
				}
			}
		})
	}
}

func TestPull_OversizedPayload_DiskExhaustionPrevention(t *testing.T) {
	expectedSize := int64(1000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Stream 100,000 bytes (far exceeding expectedSize 1,000)
		chunk := bytes.Repeat([]byte("Z"), 10000)
		for i := 0; i < 10; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer ts.Close()

	item := Item{ID: "item-oversized", Name: "big-item", Type: TypeImage}
	version := ItemVersion{ID: "v1-oversized", Version: "1.0", Format: "qcow2", Size: expectedSize, SHA256: "somehash"}

	targetDir := t.TempDir()
	tmpPath := filepath.Join(targetDir, fmt.Sprintf(".virtomir-content-%s-%s.tmp", item.ID, version.ID))

	peer := cluster.NewPeer(cluster.Node{Address: ts.URL}, ts.Client())
	_, err := Pull(context.Background(), peer, item, version, targetDir, nil)
	if err == nil {
		t.Fatalf("expected error on oversized stream, got nil")
	}
	if !strings.Contains(err.Error(), "download exceeded expected size") {
		t.Errorf("error = %q, want containing 'download exceeded expected size'", err.Error())
	}

	// Verify temp file was removed and did NOT keep growing on disk
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("oversized temp file %s was not deleted: %v", tmpPath, err)
	}
}

func TestPathTraversal_Prevention(t *testing.T) {
	targetDir := t.TempDir()
	peer := cluster.NewPeer(cluster.Node{Address: "http://127.0.0.1:9"}, nil)

	traversalInputs := []struct {
		name    string
		item    Item
		version ItemVersion
	}{
		{
			name:    "traversal in item Name",
			item:    Item{ID: "valid-id", Name: "../../evil-name", Type: TypeImage},
			version: ItemVersion{ID: "v1", Version: "1.0", Format: "qcow2", Size: 100},
		},
		{
			name:    "traversal in item ID",
			item:    Item{ID: "../evil-id", Name: "valid-name", Type: TypeImage},
			version: ItemVersion{ID: "v1", Version: "1.0", Format: "qcow2", Size: 100},
		},
		{
			name:    "traversal in version ID",
			item:    Item{ID: "valid-id", Name: "valid-name", Type: TypeImage},
			version: ItemVersion{ID: "../evil-ver-id", Version: "1.0", Format: "qcow2", Size: 100},
		},
		{
			name:    "traversal in version Version",
			item:    Item{ID: "valid-id", Name: "valid-name", Type: TypeImage},
			version: ItemVersion{ID: "v1", Version: "../../evil-version", Format: "qcow2", Size: 100},
		},
		{
			name:    "traversal in version Format",
			item:    Item{ID: "valid-id", Name: "valid-name", Type: TypeImage},
			version: ItemVersion{ID: "v1", Version: "1.0", Format: "../evil-format", Size: 100},
		},
	}

	for _, tc := range traversalInputs {
		t.Run("Pull "+tc.name, func(t *testing.T) {
			_, err := Pull(context.Background(), peer, tc.item, tc.version, targetDir, nil)
			if err == nil {
				t.Fatalf("expected path traversal error, got nil")
			}
			if !strings.Contains(err.Error(), "path traversal") {
				t.Errorf("error = %q, want containing 'path traversal'", err.Error())
			}
		})
	}

	// Test PruneLocalFile with path traversal
	pruneTests := []struct {
		name, item, version, format string
	}{
		{"item traversal", "../etc/passwd", "1.0", "qcow2"},
		{"version traversal", "ubuntu", "../1.0", "qcow2"},
		{"format traversal", "ubuntu", "1.0", "../../qcow2"},
	}
	for _, tc := range pruneTests {
		t.Run("Prune "+tc.name, func(t *testing.T) {
			err := PruneLocalFile(targetDir, tc.item, tc.version, tc.format)
			if err == nil {
				t.Fatalf("expected path traversal error, got nil")
			}
			if !strings.Contains(err.Error(), "path traversal") {
				t.Errorf("error = %q, want containing 'path traversal'", err.Error())
			}
		})
	}

	// Test DownloadPath escaping
	escapedPath := DownloadPath("item/special#1", "ver/2.0?beta")
	wantPath := "/api/v1/content/items/item%2Fspecial%231/versions/ver%2F2.0%3Fbeta/download"
	if escapedPath != wantPath {
		t.Errorf("DownloadPath = %q, want %q", escapedPath, wantPath)
	}
}

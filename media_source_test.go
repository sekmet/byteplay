package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
)

func TestMediaSourceRequiresMetadata(t *testing.T) {
	if _, err := newMediaSource(nil, 0); err == nil {
		t.Fatal("nil torrent must not be accessed")
	}
}

type syntheticReader struct {
	*bytes.Reader
	readahead int64
	ctx       context.Context
}

func (reader *syntheticReader) Close() error                           { return nil }
func (reader *syntheticReader) SetContext(ctx context.Context)         { reader.ctx = ctx }
func (reader *syntheticReader) SetReadahead(n int64)                   { reader.readahead = n }
func (reader *syntheticReader) SetReadaheadFunc(torrent.ReadaheadFunc) {}
func (reader *syntheticReader) SetResponsive()                         {}
func (reader *syntheticReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	return reader.Read(p)
}

func TestMediaReaderCountsDeliveredBytesAndFirstByte(t *testing.T) {
	inner := &syntheticReader{Reader: bytes.NewReader([]byte("abcdef"))}
	reader := &MediaReader{
		Reader:    inner,
		startedAt: time.Now().Add(-20 * time.Millisecond),
	}
	if reader.bytesServed.Load() != 0 || reader.firstByteUnixNano.Load() != 0 {
		t.Fatal("metrics must be unobserved before first read")
	}
	buf := make([]byte, 3)
	if n, err := reader.Read(buf); n != 3 || err != nil || string(buf) != "abc" {
		t.Fatalf("unexpected first read n=%d err=%v data=%q", n, err, buf)
	}
	first := reader.firstByteUnixNano.Load()
	if first == 0 || reader.bytesServed.Load() != 3 {
		t.Fatal("observed metrics not recorded")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := reader.Read(buf); n != 3 || err != nil || string(buf) != "abc" {
		t.Fatalf("unexpected second read n=%d err=%v data=%q", n, err, buf)
	}
	if reader.bytesServed.Load() != 6 || reader.firstByteUnixNano.Load() != first {
		t.Fatal("seek must not reset first-byte time or delivered-byte total")
	}
}

package main

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
)

const mediaSourceReadahead int64 = 16 << 20

// MediaSource serves only the selected torrent file, leaving piece priority
// under the existing anacrolix Reader. It never calls Download/DownloadAll.
type MediaSource struct {
	file    *torrent.File
	torrent *torrent.Torrent
	index   int
}

type MediaSourceProgress struct {
	FileIndex              int
	FileBytesTotal         int64
	FileBytesCompleted     int64
	TorrentBytesTotal      int64
	TorrentBytesCompleted  int64
	BytesServed            int64
	FirstByteLatencyMillis *int64
	ServedBytesPerSecond   float64
}

func newMediaSource(t *torrent.Torrent, index int) (MediaSource, error) {
	if t == nil || t.Info() == nil {
		return MediaSource{}, errors.New("torrent metadata not available")
	}
	files := t.Files()
	if index < 0 || index >= len(files) {
		return MediaSource{}, errors.New("file index out of range")
	}
	return MediaSource{file: files[index], torrent: t, index: index}, nil
}

func (source MediaSource) Open(ctx context.Context) *MediaReader {
	reader := source.file.NewReader()
	reader.SetContext(ctx)
	reader.SetReadahead(mediaSourceReadahead)
	return &MediaReader{
		Reader:    reader,
		source:    source,
		startedAt: time.Now(),
	}
}

func (source MediaSource) Progress() MediaSourceProgress {
	return MediaSourceProgress{
		FileIndex:             source.index,
		FileBytesTotal:        source.file.Length(),
		FileBytesCompleted:    source.file.BytesCompleted(),
		TorrentBytesTotal:     source.torrent.Length(),
		TorrentBytesCompleted: source.torrent.BytesCompleted(),
	}
}

// Anacrolix readers are not safe for concurrent Read/Seek. Only Progress may
// be queried concurrently; the counters are atomic for diagnostic sampling.
type MediaReader struct {
	torrent.Reader
	source            MediaSource
	startedAt         time.Time
	firstByteUnixNano atomic.Int64
	bytesServed       atomic.Int64
}

func (reader *MediaReader) Read(p []byte) (int, error) {
	n, err := reader.Reader.Read(p)
	if n > 0 {
		reader.firstByteUnixNano.CompareAndSwap(0, time.Now().UnixNano())
		reader.bytesServed.Add(int64(n))
	}
	return n, err
}

func (reader *MediaReader) Progress() MediaSourceProgress {
	progress := reader.source.Progress()
	progress.BytesServed = reader.bytesServed.Load()
	if first := reader.firstByteUnixNano.Load(); first != 0 {
		latency := time.Unix(0, first).Sub(reader.startedAt).Milliseconds()
		progress.FirstByteLatencyMillis = &latency
	}
	if progress.BytesServed > 0 {
		seconds := time.Since(reader.startedAt).Seconds()
		if seconds > 0 {
			progress.ServedBytesPerSecond = float64(progress.BytesServed) / seconds
		}
	}
	return progress
}

var _ io.ReadSeekCloser = (*MediaReader)(nil)

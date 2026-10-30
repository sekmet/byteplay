package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func TestTorrentStorageRejectsMmapWholeFileBeforeDiskExpansion(t *testing.T) {
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	info := &metainfo.Info{PieceLength: 64, Name: "huge.mkv", Length: 384, Pieces: make([]byte, 6*20)}
	store := newBudgetTorrentStorage(m.torrentRoot, m)
	defer store.Close()
	torrent, err := store.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	defer torrent.Close()
	m.cacheLimit = dirSize(m.torrentRoot) + 128
	piece := torrent.Piece(info.Piece(0))
	if n, err := piece.WriteAt(bytes.Repeat([]byte("a"), 32), 0); !errors.Is(err, errTorrentCacheFull) || n != 0 {
		t.Fatalf("mmap file preallocation escaped cache budget: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(m.torrentRoot, "huge.mkv.part")); !os.IsNotExist(err) {
		t.Fatalf("denied whole-file allocation created part file: %v", err)
	}
}

func TestTorrentStorageAllowsSmallFileAndRejectsLargePeerFile(t *testing.T) {
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	info := &metainfo.Info{PieceLength: 64, Name: "collection", Files: []metainfo.FileInfo{
		{Path: []string{"clip.mp4"}, Length: 64},
		{Path: []string{"large.mkv"}, Length: 320},
	}, Pieces: make([]byte, 6*20)}
	store := newBudgetTorrentStorage(m.torrentRoot, m)
	defer store.Close()
	torrent, err := store.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	defer torrent.Close()
	m.cacheLimit = dirSize(m.torrentRoot) + 128
	first := torrent.Piece(info.Piece(0))
	if n, err := first.WriteAt(bytes.Repeat([]byte("a"), 32), 0); err != nil || n != 32 {
		t.Fatalf("small file rejected: %d %v", n, err)
	}
	large := torrent.Piece(info.Piece(5))
	if n, err := large.WriteAt(bytes.Repeat([]byte("z"), 32), 0); !errors.Is(err, errTorrentCacheFull) || n != 0 {
		t.Fatalf("large file preallocation admitted: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(m.torrentRoot, "collection", "large.mkv.part")); !os.IsNotExist(err) {
		t.Fatalf("denied large file was created: %v", err)
	}
	small, err := os.Stat(filepath.Join(m.torrentRoot, "collection", "clip.mp4.part"))
	if err != nil || small.Size() != 64 {
		t.Fatalf("small file should be mmap-allocated to 64 bytes: %v %v", small, err)
	}
	if n, err := first.WriteAt(bytes.Repeat([]byte("b"), 32), 32); err != nil || n != 32 {
		t.Fatalf("remaining bytes of admitted small file rejected: %d %v", n, err)
	}
}

func TestTorrentWritesShareHeadroomWithHLSReservation(t *testing.T) {
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	m.reserveBytes = 144
	info := &metainfo.Info{PieceLength: 64, Name: "clips", Files: []metainfo.FileInfo{
		{Path: []string{"first.mp4"}, Length: 64},
		{Path: []string{"second.mp4"}, Length: 160},
	}, Pieces: make([]byte, 4*20)}
	store := newBudgetTorrentStorage(m.torrentRoot, m)
	defer store.Close()
	torrent, err := store.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	defer torrent.Close()
	m.cacheLimit = dirSize(m.torrentRoot) + 224
	if !m.reserveCache(filepath.Join(m.root, "reserved")) {
		t.Fatal("HLS reservation not admitted")
	}
	first := torrent.Piece(info.Piece(0))
	if n, err := first.WriteAt(make([]byte, 48), 0); err != nil || n != 48 {
		t.Fatalf("64-byte file should fit with 144-byte HLS reservation: %d %v", n, err)
	}
	second := torrent.Piece(info.Piece(1))
	if n, err := second.WriteAt(make([]byte, 16), 0); !errors.Is(err, errTorrentCacheFull) || n != 0 {
		t.Fatalf("second file exceeded HLS reserved headroom: %d %v", n, err)
	}
	m.releaseCache(filepath.Join(m.root, "reserved"))
	if n, err := second.WriteAt(make([]byte, 16), 0); err != nil || n != 16 {
		t.Fatalf("released HLS capacity not reusable for second file: %d %v", n, err)
	}
}

func TestTorrentWriteRejectsMetadataPathTraversal(t *testing.T) {
	info := &metainfo.Info{PieceLength: 64, Name: "../outside", Length: 64, Pieces: make([]byte, 20)}
	_, err := torrentProjectedFileEnds(t.TempDir(), info.Piece(0), 0, 16)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("unsafe torrent file path not rejected: %v", err)
	}
}

func TestTorrentPieceProjectionAcrossMultipleFiles(t *testing.T) {
	info := &metainfo.Info{PieceLength: 128, Name: "album", Files: []metainfo.FileInfo{
		{Path: []string{"a.mkv"}, Length: 70},
		{Path: []string{"b.mkv"}, Length: 90},
	}, Pieces: make([]byte, 40)}
	ends, err := torrentProjectedFileEnds(t.TempDir(), info.Piece(0), 60, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(ends) != 2 {
		t.Fatalf("piece should span two files: %v", ends)
	}
	for name, size := range ends {
		if strings.HasSuffix(name, "a.mkv.part") && size != 70 {
			t.Fatalf("first file should end at 70, got %d", size)
		}
		if strings.HasSuffix(name, "b.mkv.part") && size != 90 {
			t.Fatalf("second file must reserve full 90 bytes, got %d", size)
		}
	}
}

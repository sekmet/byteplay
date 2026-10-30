package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

// Run in a *new process* with TORRENT_STORAGE_DEFAULT_FILE_IO=classic:
// anacrolix selects its file backend during package init, not during a test.
func TestTorrentStorageClassicSparseFileBelowFullFileBudget(t *testing.T) {
	if os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO") != "classic" {
		t.Skip("requires classic file backend selected before process startup")
	}
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	info := &metainfo.Info{PieceLength: 64, Name: "big-video.mp4",
		Length: 384, Pieces: make([]byte, 6*20)}
	store := newBudgetTorrentStorage(m.torrentRoot, m)
	defer store.Close()
	torrent, err := store.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	defer torrent.Close()
	m.cacheLimit = dirSize(m.torrentRoot) + 128
	first := torrent.Piece(info.Piece(0))
	if n, err := first.WriteAt(bytes.Repeat([]byte("x"), 32), 0); err != nil || n != 32 {
		t.Fatalf("first sparse write rejected although declared file larger than budget: %d %v", n, err)
	}
	part := filepath.Join(m.torrentRoot, "big-video.mp4.part")
	stat, err := os.Stat(part)
	if err != nil || stat.Size() != 32 {
		t.Fatalf("classic backend did not preserve partial length: %v %v", stat, err)
	}
	far := torrent.Piece(info.Piece(5))
	if n, err := far.WriteAt(bytes.Repeat([]byte("z"), 32), 0); !errors.Is(err, errTorrentCacheFull) || n != 0 {
		t.Fatalf("far extent would exceed apparent-size quota: n=%d err=%v", n, err)
	}
	stat, err = os.Stat(part)
	if err != nil || stat.Size() != 32 {
		t.Fatalf("rejected write expanded file: %v %v", stat, err)
	}
	if n, err := first.WriteAt(bytes.Repeat([]byte("y"), 32), 32); err != nil || n != 32 {
		t.Fatalf("adjacent valid bytes rejected: %d %v", n, err)
	}
	stat, err = os.Stat(part)
	if err != nil || stat.Size() != 64 {
		t.Fatalf("adjacent write wrong file length: %v %v", stat, err)
	}
}

func TestTorrentStorageClassicSharesQuotaWithHLS(t *testing.T) {
	if os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO") != "classic" {
		t.Skip("requires classic backend")
	}
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	m.reserveBytes = 144
	info := &metainfo.Info{PieceLength: 64, Name: "sparse.mkv", Length: 512, Pieces: make([]byte, 8*20)}
	store := newBudgetTorrentStorage(m.torrentRoot, m)
	defer store.Close()
	torrent, err := store.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	defer torrent.Close()
	m.cacheLimit = dirSize(m.torrentRoot) + 200
	reserved := filepath.Join(m.root, "existing-hls")
	if !m.reserveCache(reserved) {
		t.Fatal("HLS reservation not admitted")
	}
	piece := torrent.Piece(info.Piece(0))
	if n, err := piece.WriteAt(make([]byte, 48), 0); err != nil || n != 48 {
		t.Fatalf("48 bytes should fit: %d %v", n, err)
	}
	if n, err := piece.WriteAt(make([]byte, 16), 48); !errors.Is(err, errTorrentCacheFull) || n != 0 {
		t.Fatalf("16 byte growth would exceed shared HLS headroom: %d %v", n, err)
	}
	m.releaseCache(reserved)
	if n, err := piece.WriteAt(make([]byte, 16), 48); err != nil || n != 16 {
		t.Fatalf("capacity not reusable: %d %v", n, err)
	}
}

func TestTorrentStorageClassicProjectionAcrossTwoFiles(t *testing.T) {
	info := &metainfo.Info{PieceLength: 128, Name: "album",
		Files:  []metainfo.FileInfo{{Path: []string{"a.mkv"}, Length: 70}, {Path: []string{"b.mkv"}, Length: 90}},
		Pieces: make([]byte, 40)}
	ends, err := torrentProjectedFileEndsWithMode(t.TempDir(), info.Piece(0), 60, 40, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ends) != 2 {
		t.Fatalf("expected 2 projected files: %v", ends)
	}
	for path, end := range ends {
		if filepath.Base(path) == "a.mkv.part" && end != 70 {
			t.Fatalf("a extent end=%d", end)
		}
		if filepath.Base(path) == "b.mkv.part" && end != 30 {
			t.Fatalf("b extent end=%d", end)
		}
	}
}

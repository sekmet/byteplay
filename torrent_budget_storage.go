package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

var errTorrentCacheFull = errors.New("torrent cache budget exhausted")

// budgetTorrentStorage guards every file-backed piece WriteAt before the
// library can extend a .part file. It uses the same mutex/limit as HLS writer
// reservations, and preserves anacrolix's completion and hashing semantics.
type budgetTorrentStorage struct {
	upstream storage.ClientImplCloser
	manager  *hlsManager
	root     string
	sparse   bool
}

func newBudgetTorrentStorage(root string, manager *hlsManager) storage.ClientImplCloser {
	return &budgetTorrentStorage{
		upstream: storage.NewFile(root),
		manager:  manager,
		root:     root,
		sparse:   os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO") == "classic",
	}
}

func (s *budgetTorrentStorage) Close() error {
	return s.upstream.Close()
}

func (s *budgetTorrentStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, hash metainfo.Hash) (storage.TorrentImpl, error) {
	opened, err := s.upstream.OpenTorrent(ctx, info, hash)
	if err != nil {
		return opened, err
	}
	original := opened.Piece
	if original == nil {
		return opened, errors.New("file storage must expose torrent pieces")
	}
	opened.Piece = func(piece metainfo.Piece) storage.PieceImpl {
		return &budgetTorrentPiece{
			PieceImpl: original(piece),
			piece:     piece,
			manager:   s.manager,
			root:      s.root,
			sparse:    s.sparse,
		}
	}
	return opened, nil
}

type budgetTorrentPiece struct {
	storage.PieceImpl
	piece   metainfo.Piece
	manager *hlsManager
	root    string
	sparse  bool
}

// A conservative apparent-file-length quota, not a physical disk-block quota.
// Default mmap TRUNCATES a part file to its FULL declared length on first
// write, while the opt-in classic file backend extends only through each
// requested write. Project the actual selected backend's growth *before*
// every write to avoid granting more space than the shared HLS ledger allows.
func (p *budgetTorrentPiece) WriteAt(data []byte, off int64) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if off < 0 || off > p.piece.Length() || int64(len(data)) > p.piece.Length()-off {
		return 0, os.ErrInvalid
	}
	ends, err := torrentProjectedFileEndsWithMode(p.root, p.piece, off, int64(len(data)), p.sparse)
	if err != nil {
		return 0, err
	}
	m := p.manager
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if m.cacheConfigErr != nil || m.cacheLimit <= 0 {
		return 0, errTorrentCacheFull
	}
	if m.torrentFileEnds == nil {
		m.torrentFileEnds = make(map[string]int64)
	}
	type previous struct {
		end    int64
		exists bool
	}
	previousEnds := make(map[string]previous, len(ends))
	for name, end := range ends {
		prior, exists := m.torrentFileEnds[name]
		previousEnds[name] = previous{prior, exists}
		if end > prior {
			m.torrentFileEnds[name] = end
		}
	}
	if m.accountedCacheBytesLocked() > m.cacheLimit {
		for name, prior := range previousEnds {
			if prior.exists {
				m.torrentFileEnds[name] = prior.end
			} else {
				delete(m.torrentFileEnds, name)
			}
		}
		return 0, errTorrentCacheFull
	}
	// Hold the same mutex until the physical write finishes. No second piece
	// can enter on projected bytes that have not yet been written.
	return p.PieceImpl.WriteAt(data, off)
}

func (p *budgetTorrentPiece) MarkComplete() error {
	m := p.manager
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if err := p.PieceImpl.MarkComplete(); err != nil {
		return err
	}
	// The file backend renames finished .part files. Once renamed, the
	// completed file's real size is already in dirSize(torrentRoot).
	for part, end := range m.torrentFileEnds {
		if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		info, err := os.Stat(strings.TrimSuffix(part, ".part"))
		if err == nil && info.Size() >= end {
			delete(m.torrentFileEnds, part)
		}
	}
	return nil
}

func (p *budgetTorrentPiece) MarkNotComplete() error {
	m := p.manager
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	return p.PieceImpl.MarkNotComplete()
}

func torrentProjectedFileEnds(root string, piece metainfo.Piece, off, length int64) (map[string]int64, error) {
	return torrentProjectedFileEndsWithMode(root, piece, off, length, false)
}

func torrentProjectedFileEndsWithMode(root string, piece metainfo.Piece, off, length int64, sparse bool) (map[string]int64, error) {
	if off < 0 || length <= 0 || length > piece.Length()-off {
		return nil, os.ErrInvalid
	}
	start := piece.Offset() + off
	end := start + length
	if end < start {
		return nil, os.ErrInvalid
	}
	info := piece.Info
	base := root
	name := info.BestName()
	if name != "" && name != metainfo.NoName {
		base = filepath.Join(root, name)
	}
	files := info.UpvertedFiles()
	projected := make(map[string]int64)
	for _, file := range files {
		fileStart := file.TorrentOffset
		fileEnd := fileStart + file.Length
		intersectionStart := max(start, fileStart)
		intersectionEnd := min(end, fileEnd)
		if intersectionStart >= intersectionEnd {
			continue
		}
		path := filepath.Join(append([]string{base}, file.BestPath()...)...) + ".part"
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("torrent piece file path escapes storage root")
		}
		growth := file.Length // mmap's openForWrite truncates the full file
		if sparse {
			growth = intersectionEnd - fileStart // classic writes only this extent
		}
		projected[path] = max(projected[path], growth)
	}
	if len(projected) == 0 {
		return nil, fmt.Errorf("torrent write range does not intersect file metadata")
	}
	return projected, nil
}

func projectedPartFileSize(name string) int64 {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

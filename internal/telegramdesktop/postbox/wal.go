package postbox

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

const (
	sqliteWALHeaderSize      = 32
	sqliteWALFrameHeaderSize = 24
	sqliteWALMagicLE         = 0x377f0682
	sqliteWALMagicBE         = 0x377f0683
)

type sqliteWALFrame struct {
	pageNo uint32
	offset int
}

// sqliteSnapshotAttempts bounds how often readSQLiteSnapshot retries when the
// database or its WAL changes while it is being read.
const sqliteSnapshotAttempts = 5

// sqliteSnapshotRetryDelay is the base back-off between snapshot attempts.
var sqliteSnapshotRetryDelay = 50 * time.Millisecond

// errSQLiteSnapshotUnstable reports that no stable main-file/WAL pair could be
// captured because the database kept changing.
var errSQLiteSnapshotUnstable = errors.New("postbox database kept changing while it was read; quit Telegram or retry the import")

// sqliteFileState identifies one version of the main database file and its WAL.
// The WAL header carries the checkpoint sequence and salts, which change on
// every WAL reset; appended frames change the WAL size.
type sqliteFileState struct {
	mainSize    int64
	mainModTime time.Time
	walExists   bool
	walSize     int64
	walModTime  time.Time
	walHeader   [sqliteWALHeaderSize]byte
	walHeaderN  int
}

func statSQLiteFiles(dbPath string) (sqliteFileState, error) {
	var state sqliteFileState
	info, err := os.Stat(dbPath)
	if err != nil {
		return state, err
	}
	state.mainSize, state.mainModTime = info.Size(), info.ModTime()
	f, err := os.Open(dbPath + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil {
		return state, err
	}
	state.walExists, state.walSize, state.walModTime = true, info.Size(), info.ModTime()
	n, err := io.ReadFull(f, state.walHeader[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return state, err
	}
	state.walHeaderN = n
	return state, nil
}

func (s sqliteFileState) equal(o sqliteFileState) bool {
	return s.mainSize == o.mainSize && s.mainModTime.Equal(o.mainModTime) &&
		s.walExists == o.walExists && s.walSize == o.walSize && s.walModTime.Equal(o.walModTime) &&
		s.walHeaderN == o.walHeaderN && s.walHeader == o.walHeader
}

// describes reports whether wal is exactly the WAL this state was taken from.
func (s sqliteFileState) describes(wal []byte, walExists bool) bool {
	if walExists != s.walExists || int64(len(wal)) != s.walSize {
		return false
	}
	return bytes.Equal(wal[:min(len(wal), sqliteWALHeaderSize)], s.walHeader[:s.walHeaderN])
}

// readSQLiteSnapshot reads a database file and its optional WAL so that both
// come from one consistent snapshot. Telegram may append frames, checkpoint,
// or reset the WAL while we read; replaying an older WAL over a newer main
// image could then produce a state that never committed. The capture is
// accepted only if neither file changed between the first and the last check
// (WAL size, modification time, header salts and checkpoint sequence, plus the
// main file size and modification time); otherwise it is retried a bounded
// number of times. If the WAL is unchanged, a concurrent checkpoint can only
// copy frames already in the captured WAL, so replaying them stays correct.
// The hook, when non-nil, runs between the steps of each attempt (tests use it
// to simulate concurrent writers).
func readSQLiteSnapshot(ctx context.Context, dbPath string, hook func(attempt int, stage string)) (mainData, walData []byte, err error) {
	for attempt := 1; attempt <= sqliteSnapshotAttempts; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(time.Duration(attempt-1) * sqliteSnapshotRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil, ctx.Err()
			case <-timer.C:
			}
		}
		before, err := statSQLiteFiles(dbPath)
		if err != nil {
			return nil, nil, err
		}
		wal, walExists, err := readOptionalSQLiteWAL(dbPath)
		if err != nil {
			return nil, nil, err
		}
		if hook != nil {
			hook(attempt, "wal")
		}
		main, err := os.ReadFile(dbPath)
		if err != nil {
			return nil, nil, err
		}
		if hook != nil {
			hook(attempt, "main")
		}
		after, err := statSQLiteFiles(dbPath)
		if err != nil {
			return nil, nil, err
		}
		if before.equal(after) && after.describes(wal, walExists) && int64(len(main)) == after.mainSize {
			return main, wal, nil
		}
	}
	return nil, nil, fmt.Errorf("%w (%d attempts)", errSQLiteSnapshotUnstable, sqliteSnapshotAttempts)
}

func readOptionalSQLiteWAL(dbPath string) ([]byte, bool, error) {
	data, err := os.ReadFile(dbPath + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// ApplySQLCipherV4WAL replays committed frames from a SQLCipher v4 write-ahead
// log onto a decrypted database image. Postbox databases run in WAL mode, so
// recently committed rows can exist only in the -wal file until Telegram
// checkpoints it. Frames are validated like SQLite WAL recovery: the header and
// frame salts and cumulative checksums must match, and only frames up to the
// last commit frame are applied. Each applied page must pass SQLCipher HMAC
// authentication. A missing, empty, reset, or unrecognized WAL leaves the image
// unchanged.
func ApplySQLCipherV4WAL(plain []byte, wal []byte, keyAndSalt []byte) ([]byte, error) {
	if len(wal) < sqliteWALHeaderSize {
		return plain, nil
	}
	magic := binary.BigEndian.Uint32(wal[0:4])
	if magic != sqliteWALMagicLE && magic != sqliteWALMagicBE {
		return plain, nil
	}
	bigEndian := magic == sqliteWALMagicBE
	pageSize := int(binary.BigEndian.Uint32(wal[8:12]))
	if pageSize < sqlcipherMinPageSize || pageSize > sqlcipherMaxPageSize || pageSize&(pageSize-1) != 0 {
		return plain, nil
	}
	s1, s2 := sqliteWALChecksum(bigEndian, wal[:24], 0, 0)
	if s1 != binary.BigEndian.Uint32(wal[24:28]) || s2 != binary.BigEndian.Uint32(wal[28:32]) {
		return plain, nil
	}
	salt := wal[16:24]

	var committed, pending []sqliteWALFrame
	var dbPages uint32
	frameSize := sqliteWALFrameHeaderSize + pageSize
	for off := sqliteWALHeaderSize; off+frameSize <= len(wal); off += frameSize {
		header := wal[off : off+sqliteWALFrameHeaderSize]
		pageNo := binary.BigEndian.Uint32(header[0:4])
		if pageNo == 0 || !bytes.Equal(header[8:16], salt) {
			break
		}
		page := wal[off+sqliteWALFrameHeaderSize : off+frameSize]
		s1, s2 = sqliteWALChecksum(bigEndian, header[:8], s1, s2)
		s1, s2 = sqliteWALChecksum(bigEndian, page, s1, s2)
		if s1 != binary.BigEndian.Uint32(header[16:20]) || s2 != binary.BigEndian.Uint32(header[20:24]) {
			break
		}
		pending = append(pending, sqliteWALFrame{pageNo: pageNo, offset: off + sqliteWALFrameHeaderSize})
		if commitPages := binary.BigEndian.Uint32(header[4:8]); commitPages != 0 {
			committed = append(committed, pending...)
			pending = pending[:0]
			dbPages = commitPages
		}
	}
	if len(committed) == 0 {
		return plain, nil
	}

	dbPageSize, err := sqlcipherPageSize(plain)
	if err != nil {
		return nil, err
	}
	if dbPageSize != pageSize {
		return nil, fmt.Errorf("sqlcipher wal page size %d does not match database page size %d", pageSize, dbPageSize)
	}
	if len(keyAndSalt) != sqlcipherKeySize+sqlcipherSaltSize {
		return nil, fmt.Errorf("sqlcipher key length = %d, want 48", len(keyAndSalt))
	}
	encKey := keyAndSalt[:sqlcipherKeySize]
	hmacKey := sqlcipherHMACKey(encKey, keyAndSalt[sqlcipherKeySize:])
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	// The commit frame's page count is not authenticated (the SQLCipher HMAC
	// covers only page contents), so bound it before allocating: every page of
	// the committed image must come from the main file or a committed frame,
	// plus at most SQLite's never-written pending-byte page.
	mainPages := uint64(len(plain) / pageSize)
	maxPages := mainPages + uint64(len(committed)) + 1
	if uint64(dbPages) > maxPages {
		return nil, fmt.Errorf("sqlcipher wal commit frame claims %d pages, but the database and wal hold at most %d", dbPages, maxPages)
	}
	imageSize := uint64(dbPages) * uint64(pageSize)
	if imageSize > uint64(math.MaxInt) {
		return nil, fmt.Errorf("sqlcipher wal image size %d overflows", imageSize)
	}
	out := make([]byte, int(imageSize))
	copy(out, plain)
	for _, frame := range committed {
		if frame.pageNo > dbPages {
			continue
		}
		page := wal[frame.offset : frame.offset+pageSize]
		start := int(frame.pageNo-1) * pageSize
		dst := out[start : start+pageSize]
		offset := 0
		if frame.pageNo == 1 {
			offset = sqlcipherPlainHeader
			copy(dst[:offset], page[:offset])
		}
		if err := decryptSQLCipherPage(block, hmacKey, frame.pageNo, pageSize, page[offset:], dst[offset:]); err != nil {
			zeroBytes(out)
			return nil, fmt.Errorf("sqlcipher wal: %w", err)
		}
	}
	return out, nil
}

// sqliteWALChecksum implements SQLite's cumulative WAL checksum over 32-bit
// words in the byte order selected by the WAL header magic.
func sqliteWALChecksum(bigEndian bool, data []byte, s1, s2 uint32) (uint32, uint32) {
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	for i := 0; i+8 <= len(data); i += 8 {
		s1 += order.Uint32(data[i:]) + s2
		s2 += order.Uint32(data[i+4:]) + s1
	}
	return s1, s2
}

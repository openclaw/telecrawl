package postbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/pbkdf2"
)

const (
	sqliteHeader           = "SQLite format 3\x00"
	sqlcipherPlainHeader   = 32
	sqlcipherKeySize       = 32
	sqlcipherSaltSize      = 16
	sqlcipherIVSize        = aes.BlockSize
	sqlcipherHMACSize      = 64
	sqlcipherReserveSize   = 80
	sqlcipherFastKDFIter   = 2
	sqlcipherHMACSaltMask  = 0x3a
	sqlcipherMinPageSize   = 512
	sqlcipherMaxPageSize   = 65536
	sqlcipherDefaultPageSz = 4096
)

func DecryptSQLCipherV4(data []byte, keyAndSalt []byte) ([]byte, error) {
	if len(keyAndSalt) != sqlcipherKeySize+sqlcipherSaltSize {
		return nil, fmt.Errorf("sqlcipher key length = %d, want 48", len(keyAndSalt))
	}
	if len(data) < sqlcipherPlainHeader {
		return nil, fmt.Errorf("sqlcipher database too small: %d bytes", len(data))
	}
	pageSize, err := sqlcipherPageSize(data)
	if err != nil {
		return nil, err
	}
	if len(data)%pageSize != 0 {
		return nil, fmt.Errorf("sqlcipher database size %d is not a multiple of page size %d", len(data), pageSize)
	}
	if pageSize <= sqlcipherReserveSize+sqlcipherPlainHeader {
		return nil, fmt.Errorf("sqlcipher page size %d is too small", pageSize)
	}
	encKey := keyAndSalt[:sqlcipherKeySize]
	salt := keyAndSalt[sqlcipherKeySize:]
	hmacKey := sqlcipherHMACKey(encKey, salt)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	pageCount := len(data) / pageSize
	for i := 0; i < pageCount; i++ {
		pageNo := uint32(i + 1)
		page := data[i*pageSize : (i+1)*pageSize]
		dst := out[i*pageSize : (i+1)*pageSize]
		offset := 0
		if pageNo == 1 {
			offset = sqlcipherPlainHeader
			copy(dst[:offset], page[:offset])
		}
		if err := decryptSQLCipherPage(block, hmacKey, pageNo, page[offset:], dst[offset:]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sqlcipherPageSize(data []byte) (int, error) {
	if len(data) >= len(sqliteHeader) && string(data[:len(sqliteHeader)]) == sqliteHeader {
		raw := int(binary.BigEndian.Uint16(data[16:18]))
		switch raw {
		case 1:
			return 65536, nil
		case 0:
			return 0, fmt.Errorf("invalid sqlite page size 0")
		default:
			if raw < sqlcipherMinPageSize || raw > sqlcipherMaxPageSize || raw&(raw-1) != 0 {
				return 0, fmt.Errorf("invalid sqlite page size %d", raw)
			}
			return raw, nil
		}
	}
	if len(data)%sqlcipherDefaultPageSz == 0 {
		return sqlcipherDefaultPageSz, nil
	}
	return 0, fmt.Errorf("sqlcipher database does not expose a plaintext SQLite header")
}

func decryptSQLCipherPage(block cipher.Block, hmacKey []byte, pageNo uint32, page []byte, dst []byte) error {
	if len(page) != len(dst) {
		return fmt.Errorf("page output length mismatch")
	}
	payloadSize := len(page) - sqlcipherReserveSize
	if payloadSize < 0 || payloadSize%aes.BlockSize != 0 {
		return fmt.Errorf("invalid sqlcipher page payload size %d", payloadSize)
	}
	ivStart := payloadSize
	hmacStart := ivStart + sqlcipherIVSize
	hmacEnd := hmacStart + sqlcipherHMACSize
	if hmacEnd > len(page) {
		return fmt.Errorf("invalid sqlcipher reserve layout")
	}
	wantHMAC := sqlcipherPageHMAC(hmacKey, pageNo, page[:hmacStart])
	if !hmac.Equal(page[hmacStart:hmacEnd], wantHMAC) {
		return fmt.Errorf("sqlcipher hmac check failed for page %d", pageNo)
	}
	copy(dst[payloadSize:], page[payloadSize:])
	cipher.NewCBCDecrypter(block, page[ivStart:hmacStart]).CryptBlocks(dst[:payloadSize], page[:payloadSize])
	return nil
}

func sqlcipherHMACKey(encKey []byte, salt []byte) []byte {
	hmacSalt := make([]byte, len(salt))
	for i, b := range salt {
		hmacSalt[i] = b ^ sqlcipherHMACSaltMask
	}
	return pbkdf2.Key(encKey, hmacSalt, sqlcipherFastKDFIter, sqlcipherKeySize, sha512.New)
}

func sqlcipherPageHMAC(hmacKey []byte, pageNo uint32, pageAndIV []byte) []byte {
	mac := hmac.New(sha512.New, hmacKey)
	_, _ = mac.Write(pageAndIV)
	var rawPageNo [4]byte
	binary.LittleEndian.PutUint32(rawPageNo[:], pageNo)
	_, _ = mac.Write(rawPageNo[:])
	return mac.Sum(nil)
}

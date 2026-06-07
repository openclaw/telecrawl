package postbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

const tempKeyMurmurSeed uint32 = 0xf7ca7fd2

var DefaultPasscodes = [][]byte{[]byte("no-matter-key"), {}}

func ReadTempKey(path string, passcodes [][]byte) ([]byte, error) {
	encrypted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecryptTempKey(encrypted, passcodes)
}

func DecryptTempKey(encrypted []byte, passcodes [][]byte) ([]byte, error) {
	if len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid tempkey size: %d", len(encrypted))
	}
	if len(passcodes) == 0 {
		passcodes = DefaultPasscodes
	}
	for _, passcode := range passcodes {
		key, iv := tempKeyCipher(passcode)
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		data := make([]byte, len(encrypted))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, encrypted)
		if len(data) < 52 {
			continue
		}
		dbKeyAndSalt := data[:48]
		expected := int32(binary.LittleEndian.Uint32(data[48:52]))
		if murmur3_32(dbKeyAndSalt, tempKeyMurmurSeed) == expected {
			out := make([]byte, len(dbKeyAndSalt))
			copy(out, dbKeyAndSalt)
			return out, nil
		}
	}
	return nil, errors.New("unable to decrypt tempkey")
}

func tempKeyCipher(passcode []byte) (key []byte, iv []byte) {
	digest := sha512.Sum512(passcode)
	return digest[:32], digest[48:]
}

func murmur3_32(data []byte, seed uint32) int32 {
	length := len(data)
	h1 := seed
	const c1 uint32 = 0xcc9e2d51
	const c2 uint32 = 0x1b873593
	roundedEnd := length & 0xfffffffc
	for i := 0; i < roundedEnd; i += 4 {
		k1 := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
		h1 = (h1 << 13) | (h1 >> 19)
		h1 = h1*5 + 0xe6546b64
	}
	var k1 uint32
	switch length & 3 {
	case 3:
		k1 ^= uint32(data[roundedEnd+2]) << 16
		fallthrough
	case 2:
		k1 ^= uint32(data[roundedEnd+1]) << 8
		fallthrough
	case 1:
		k1 ^= uint32(data[roundedEnd])
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
	}
	h1 ^= uint32(length)
	h1 ^= h1 >> 16
	h1 *= 0x85ebca6b
	h1 ^= h1 >> 13
	h1 *= 0xc2b2ae35
	h1 ^= h1 >> 16
	return int32(h1)
}

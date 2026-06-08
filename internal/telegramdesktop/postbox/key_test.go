package postbox

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestMurmur3MatchesFormerBridgeVectors(t *testing.T) {
	if got := murmur3_32(nil, tempKeyMurmurSeed); got != 377927480 {
		t.Fatalf("empty murmur = %d", got)
	}
	if got := murmur3_32([]byte("fixture"), tempKeyMurmurSeed); got != 195095184 {
		t.Fatalf("fixture murmur = %d", got)
	}
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	if got := murmur3_32(keyAndSalt, tempKeyMurmurSeed); got != 1204842331 {
		t.Fatalf("key murmur = %d", got)
	}
}

func TestDecryptTempKey(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	encrypted := encryptedTempKeyFixture(t, []byte("passcode"), keyAndSalt)
	got, err := DecryptTempKey(encrypted, [][]byte{[]byte("wrong"), []byte("passcode")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(keyAndSalt) {
		t.Fatalf("decrypted key = %x, want %x", got, keyAndSalt)
	}
	if _, err := DecryptTempKey(encrypted, [][]byte{[]byte("wrong")}); err == nil {
		t.Fatal("wrong passcode decrypted tempkey")
	}
	if _, err := DecryptTempKey(encrypted[:len(encrypted)-1], [][]byte{[]byte("passcode")}); err == nil {
		t.Fatal("invalid tempkey size succeeded")
	}
}

func TestReadTempKey(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(255 - i)
	}
	path := filepath.Join(t.TempDir(), ".tempkeyEncrypted")
	if err := os.WriteFile(path, encryptedTempKeyFixture(t, []byte("no-matter-key"), keyAndSalt), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTempKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(keyAndSalt) {
		t.Fatalf("read tempkey = %x, want %x", got, keyAndSalt)
	}
}

func encryptedTempKeyFixture(t *testing.T, passcode []byte, keyAndSalt []byte) []byte {
	t.Helper()
	if len(keyAndSalt) != 48 {
		t.Fatalf("keyAndSalt length = %d, want 48", len(keyAndSalt))
	}
	plain := make([]byte, 64)
	copy(plain, keyAndSalt)
	binary.LittleEndian.PutUint32(plain[48:52], uint32(murmur3_32(keyAndSalt, tempKeyMurmurSeed)))
	key, iv := tempKeyCipher(passcode)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return out
}

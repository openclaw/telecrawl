package backup

import ckbackup "github.com/openclaw/crawlkit/backup"

func EnsureIdentity(path string) (string, error) {
	return ckbackup.EnsureIdentity(path)
}

func RecipientFromIdentity(path string) (string, error) {
	return ckbackup.RecipientFromIdentity(path)
}

func encryptShard(plaintext []byte, recipientStrings []string) ([]byte, string, error) {
	return ckbackup.EncryptShard(plaintext, recipientStrings)
}

func decryptShard(ciphertext []byte, identityPath string) ([]byte, error) {
	return ckbackup.DecryptShard(ciphertext, identityPath)
}

func sha256Hex(data []byte) string {
	return ckbackup.SHA256Hex(data)
}

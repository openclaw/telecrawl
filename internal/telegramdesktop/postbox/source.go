package postbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Source struct {
	AccountID string
	KeyPath   string
	DBPath    string
}

type NativeSession struct {
	AccountID string
	DCID      int
	AuthKey   []byte
	Host      string
	Port      int
}

var dcEndpoints = map[int]struct {
	host string
	port int
}{
	1: {"149.154.175.53", 443},
	2: {"149.154.167.51", 443},
	3: {"149.154.175.100", 443},
	4: {"149.154.167.91", 443},
	5: {"91.108.56.130", 443},
}

func DefaultGroupPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Group Containers", "6N38VWS5BX.ru.keepcoder.Telegram")
}

func DiscoverSources(sourceArg string) ([]Source, error) {
	root := DefaultGroupPath()
	if strings.TrimSpace(sourceArg) != "" {
		expanded, err := expandHome(strings.TrimSpace(sourceArg))
		if err != nil {
			return nil, err
		}
		root = expanded
	}
	if fileExists(filepath.Join(root, "postbox", "db", "db_sqlite")) {
		return []Source{{
			AccountID: filepath.Base(root),
			KeyPath:   filepath.Join(filepath.Dir(root), ".tempkeyEncrypted"),
			DBPath:    filepath.Join(root, "postbox", "db", "db_sqlite"),
		}}, nil
	}

	var lanes []string
	if fileExists(filepath.Join(root, ".tempkeyEncrypted")) {
		lanes = append(lanes, root)
	}
	for _, name := range []string{"stable", "appstore"} {
		lane := filepath.Join(root, name)
		if fileExists(filepath.Join(lane, ".tempkeyEncrypted")) {
			lanes = append(lanes, lane)
		}
	}
	if len(lanes) == 0 {
		entries, err := os.ReadDir(root)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				lane := filepath.Join(root, entry.Name())
				if fileExists(filepath.Join(lane, ".tempkeyEncrypted")) {
					lanes = append(lanes, lane)
				}
			}
		}
	}
	lanes = uniqueSorted(lanes)
	var sources []Source
	for _, lane := range lanes {
		entries, err := os.ReadDir(lane)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "account-") {
				continue
			}
			accountPath := filepath.Join(lane, entry.Name())
			dbPath := filepath.Join(accountPath, "postbox", "db", "db_sqlite")
			keyPath := filepath.Join(lane, ".tempkeyEncrypted")
			if fileExists(keyPath) && fileExists(dbPath) {
				sources = append(sources, Source{
					AccountID: filepath.Base(lane) + "/" + entry.Name(),
					KeyPath:   keyPath,
					DBPath:    dbPath,
				})
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].AccountID < sources[j].AccountID
	})
	return sources, nil
}

func NativeSessionForSource(source Source) (*NativeSession, error) {
	accountPath := filepath.Dir(filepath.Dir(filepath.Dir(source.DBPath)))
	lanePath := filepath.Dir(accountPath)
	accountRecordID, err := AccountDirRecordID(filepath.Base(accountPath))
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(lanePath, "accounts-shared-data"))
	if err != nil {
		return nil, nil
	}
	var shared map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&shared); err != nil {
		return nil, nil
	}
	accounts, _ := shared["accounts"].([]any)
	for _, rawAccount := range accounts {
		account, _ := rawAccount.(map[string]any)
		if account == nil {
			continue
		}
		id, ok := int64Value(account["id"])
		if !ok || id != accountRecordID {
			continue
		}
		dcID64, ok := int64Value(account["primaryId"])
		if !ok {
			return nil, nil
		}
		dcID := int(dcID64)
		endpoint, ok := dcEndpoints[dcID]
		if !ok {
			return nil, nil
		}
		datacenters := dictPairs(account["datacenters"])
		datacenter, _ := datacenters[strconv.Itoa(dcID)].(map[string]any)
		if datacenter == nil {
			return nil, nil
		}
		masterKey, _ := datacenter["masterKey"].(map[string]any)
		keyData := strings.TrimSpace(stringValue(masterKey["data"]))
		if keyData == "" {
			return nil, nil
		}
		authKey, err := base64.StdEncoding.DecodeString(keyData)
		if err != nil || len(authKey) != 256 {
			return nil, nil
		}
		keyCopy := make([]byte, len(authKey))
		copy(keyCopy, authKey)
		return &NativeSession{
			AccountID: source.AccountID,
			DCID:      dcID,
			AuthKey:   keyCopy,
			Host:      endpoint.host,
			Port:      endpoint.port,
		}, nil
	}
	return nil, nil
}

func dictPairs(value any) map[string]any {
	out := make(map[string]any)
	switch typed := value.(type) {
	case map[string]any:
		for key, value := range typed {
			out[key] = value
		}
	case []any:
		for i := 0; i+1 < len(typed); i += 2 {
			out[stringValue(typed[i])] = typed[i+1]
		}
	}
	return out
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	var last string
	for _, value := range values {
		if value == last {
			continue
		}
		out = append(out, value)
		last = value
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

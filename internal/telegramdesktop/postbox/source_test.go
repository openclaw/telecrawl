package postbox

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestDiscoverSourcesFindsNativeLanes(t *testing.T) {
	root := t.TempDir()
	stable := makePostboxSourceFixture(t, root, "stable", "account-1")
	appstore := makePostboxSourceFixture(t, root, "appstore", "account-2")

	sources, err := DiscoverSources(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{
		{AccountID: "appstore/account-2", KeyPath: appstore.KeyPath, DBPath: appstore.DBPath},
		{AccountID: "stable/account-1", KeyPath: stable.KeyPath, DBPath: stable.DBPath},
	}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %#v, want %#v", sources, want)
	}
}

func TestDiscoverSourcesClassifiesAccountPath(t *testing.T) {
	root := t.TempDir()
	source := makePostboxSourceFixture(t, root, "stable", "account-1")
	accountPath := filepath.Dir(filepath.Dir(filepath.Dir(source.DBPath)))

	sources, err := DiscoverSources(accountPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{{
		AccountID: "account-1",
		KeyPath:   source.KeyPath,
		DBPath:    source.DBPath,
	}}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %#v, want %#v", sources, want)
	}
}

func TestNativeSessionForSource(t *testing.T) {
	root := t.TempDir()
	source := makePostboxSourceFixture(t, root, "stable", "account-10833815886710207757")
	accountID, err := AccountDirRecordID("account-10833815886710207757")
	if err != nil {
		t.Fatal(err)
	}
	authKey := make([]byte, 256)
	for i := range authKey {
		authKey[i] = byte(i)
	}
	shared := map[string]any{
		"accounts": []any{
			map[string]any{
				"id":        json.Number("-1"),
				"primaryId": json.Number("1"),
				"datacenters": []any{
					json.Number("1"),
					map[string]any{"masterKey": map[string]any{"data": ""}},
				},
			},
			map[string]any{
				"id":        json.Number(accountIDString(accountID)),
				"primaryId": json.Number("2"),
				"datacenters": []any{
					json.Number("2"),
					map[string]any{
						"masterKey": map[string]any{
							"data": base64.StdEncoding.EncodeToString(authKey),
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	lanePath := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(source.DBPath))))
	if err := os.WriteFile(filepath.Join(lanePath, "accounts-shared-data"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := NativeSessionForSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if session == nil {
		t.Fatal("native session not found")
	}
	if session.AccountID != source.AccountID || session.DCID != 2 || session.Host != "149.154.167.51" || session.Port != 443 {
		t.Fatalf("session metadata = %+v", session)
	}
	if !reflect.DeepEqual(session.AuthKey, authKey) {
		t.Fatal("auth key mismatch")
	}
}

func makePostboxSourceFixture(t *testing.T, root, laneName, accountName string) Source {
	t.Helper()
	lane := filepath.Join(root, laneName)
	account := filepath.Join(lane, accountName)
	dbPath := filepath.Join(account, "postbox", "db", "db_sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(lane, ".tempkeyEncrypted")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{AccountID: laneName + "/" + accountName, KeyPath: keyPath, DBPath: dbPath}
}

func accountIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

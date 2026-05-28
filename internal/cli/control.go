package cli

import (
	"path/filepath"

	"github.com/openclaw/crawlkit/control"
)

func controlManifest() control.Manifest {
	m := control.NewManifest("telecrawl", "Telegram Crawl", "telecrawl")
	m.Description = "Local-first Telegram archive crawler."
	m.Branding = control.Branding{SymbolName: "paperplane.fill", AccentColor: "#229ed9", BundleIdentifier: "org.telegram.desktop"}
	m.Paths = control.Paths{
		DefaultConfig:   filepath.Join(defaultBaseDir(), "backup.toml"),
		DefaultDatabase: defaultDBPath(),
		DefaultCache:    filepath.Join(defaultBaseDir(), "cache"),
		DefaultLogs:     filepath.Join(defaultBaseDir(), "logs"),
	}
	m.Capabilities = []string{"metadata", "doctor", "status", "sync", "search", "backup"}
	m.Privacy = control.Privacy{ContainsPrivateMessages: true, ExportsSecrets: false, LocalOnlyScopes: []string{"telegram-desktop", "telegram-macos-postbox", "sqlite", "encrypted-git-backup"}}
	m.Commands = map[string]control.Command{
		"doctor": {Title: "Doctor", Argv: []string{"telecrawl", "--json", "doctor"}, JSON: true},
		"status": {Title: "Status", Argv: []string{"telecrawl", "--json", "status"}, JSON: true},
		"sync":   {Title: "Import", Argv: []string{"telecrawl", "--json", "import"}, JSON: true, Mutates: true},
		"search": {Title: "Search", Argv: []string{"telecrawl", "--json", "search"}, JSON: true},
	}
	return m
}

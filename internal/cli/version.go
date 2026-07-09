package cli

import (
	"runtime/debug"
	"strings"
)

var version = "dev"

func currentVersion() string {
	moduleVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
	}
	return resolveVersion(version, moduleVersion)
}

func resolveVersion(linkedVersion, moduleVersion string) string {
	if linkedVersion != "" && linkedVersion != "dev" {
		return linkedVersion
	}
	if moduleVersion != "" && moduleVersion != "(devel)" {
		return strings.TrimPrefix(moduleVersion, "v")
	}
	return "dev"
}

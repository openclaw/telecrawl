package backup

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Wait for Git maintenance before temporary repositories are cleaned up.
	params := os.Getenv("GIT_CONFIG_PARAMETERS")
	if params != "" {
		params += " "
	}
	params += "'gc.autoDetach=false' 'maintenance.autoDetach=false'"
	if err := os.Setenv("GIT_CONFIG_PARAMETERS", params); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

//go:build !windows

package localfile

import (
	"os"
	"syscall"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW

package testutil

import (
	"os"
	"runtime"
	"testing"
)

func RequiresRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("test requires root")
	}
}

func RequiresLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}

func RequiresLinuxSupport(t *testing.T, subsystem string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("Depends on unimplemented %s support on %s", subsystem, runtime.GOOS)
	}
}

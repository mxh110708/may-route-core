package main

import (
	"os"
	"testing"
)

// Opt-in regression using real signed PE version resources, without launching an installer.
func TestDesktopUpdateExecutableVersions(t *testing.T) {
	installedPath, installerPath := os.Getenv("MAY_TEST_INSTALLED_EXE"), os.Getenv("MAY_TEST_INSTALLER_EXE")
	if installedPath == "" || installerPath == "" {
		t.Skip("executable fixtures not supplied")
	}
	installed, err := windowsExecutableIdentity(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := windowsExecutableIdentity(installerPath)
	if err != nil {
		t.Fatal(err)
	}
	if installed.productName != updateProductName || candidate.productName != updateProductName {
		t.Fatal("product mismatch")
	}
	if !isNewerDesktopVersion(candidate.version, installed.version) {
		t.Fatalf("rejected %s -> %s", installed.version, candidate.version)
	}
	if isNewerDesktopVersion(installed.version, candidate.version) {
		t.Fatal("downgrade accepted")
	}
	t.Logf("PE metadata upgrade accepted: %s -> %s; downgrade rejected", installed.version, candidate.version)
}

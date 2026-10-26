package deploy

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeBinary = "#!/bin/sh\necho fake up\n"

// release builds a fake release directory for the host and returns its URL.
func release(t *testing.T, binary string, checksum func(archive []byte) string) string {
	t.Helper()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("install.sh supports linux amd64/arm64 only")
	}
	dir := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "up"), []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "up_linux_" + runtime.GOARCH + ".tar.gz"
	if out, err := exec.Command("tar", "-czf", filepath.Join(dir, name), "-C", src, "up").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v: %s", err, out)
	}
	archive, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	sums := fmt.Sprintf("%s  %s\n", checksum(archive), name)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + dir
}

func goodSum(archive []byte) string { return fmt.Sprintf("%x", sha256.Sum256(archive)) }

func install(t *testing.T, baseURL string, installDir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"../install.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"UP_BASE_URL="+baseURL,
		"UP_INSTALL_DIR="+installDir,
		"UP_UNIT_PATH="+filepath.Join(t.TempDir(), "up.service"),
	)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errOut.String()
}

func TestInstallPlacesVerifiedBinary(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := install(t, release(t, fakeBinary, goodSum), dir)
	if code != 0 || errOut != "" || !strings.Contains(out, "Installed "+dir+"/up") {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	info, err := os.Stat(filepath.Join(dir, "up"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary missing or wrong mode: %v, %v", info, err)
	}
	got, err := exec.Command(filepath.Join(dir, "up")).Output()
	if err != nil || string(got) != "fake up\n" {
		t.Fatalf("installed binary ran as %q, %v", got, err)
	}
}

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	bad := func([]byte) string { return strings.Repeat("0", 64) }
	code, _, errOut := install(t, release(t, fakeBinary, bad), dir)
	if code != 1 || !strings.Contains(errOut, "checksum mismatch") {
		t.Fatalf("got %d, %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "up")); !os.IsNotExist(err) {
		t.Fatal("binary installed despite checksum mismatch")
	}
}

func TestInstallFailsCleanlyWhenReleaseIsMissing(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := install(t, "file://"+t.TempDir(), dir)
	if code != 1 || !strings.Contains(errOut, "could not download") {
		t.Fatalf("got %d, %q", code, errOut)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("install dir not empty: %v", entries)
	}
}

func TestInstallRejectsUnknownOption(t *testing.T) {
	code, _, errOut := install(t, "file://"+t.TempDir(), t.TempDir(), "--nope")
	if code != 1 || !strings.Contains(errOut, "unknown option: --nope") {
		t.Fatalf("got %d, %q", code, errOut)
	}
}

func TestInstallHelp(t *testing.T) {
	code, out, errOut := install(t, "file://"+t.TempDir(), t.TempDir(), "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d, %q", code, errOut)
	}
	for _, want := range []string{"--service", "--version", "--uninstall", "UP_INSTALL_DIR"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q: %s", want, out)
		}
	}
}

func TestUninstallRemovesBinaryOnly(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := install(t, release(t, fakeBinary, goodSum), dir); code != 0 {
		t.Fatalf("install: %d, %q", code, errOut)
	}
	code, out, errOut := install(t, "file://"+t.TempDir(), dir, "--uninstall")
	if code != 0 || errOut != "" || !strings.Contains(out, "Removed "+dir+"/up") {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "up")); !os.IsNotExist(err) {
		t.Fatal("binary still installed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("uninstall removed an unrelated file")
	}
}

func TestInstallCreatesMissingInstallDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "bin")
	code, _, errOut := install(t, release(t, fakeBinary, goodSum), dir)
	if code != 0 || errOut != "" {
		t.Fatalf("got %d, %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "up")); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
}

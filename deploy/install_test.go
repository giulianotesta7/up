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
	unit := os.Getenv("UP_UNIT_PATH")
	if unit == "" {
		unit = filepath.Join(t.TempDir(), "up.service")
	}
	legacy := os.Getenv("UP_LEGACY_UNIT_PATH")
	if legacy == "" {
		legacy = filepath.Join(t.TempDir(), "legacy.service")
	}
	cmd.Env = append(os.Environ(),
		"UP_BASE_URL="+baseURL,
		"UP_INSTALL_DIR="+installDir,
		"UP_UNIT_PATH="+unit,
		"UP_LEGACY_UNIT_PATH="+legacy,
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

func TestUninstallUserAndLegacyUnits(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("CALL_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, body := range map[string]string{
		"systemctl": "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
		"sudo":      "#!/bin/sh\nprintf 'sudo %s\\n' \"$*\" >>\"$CALL_LOG\"\nexec \"$@\"\n",
		"loginctl":  "#!/bin/sh\nexit 99\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	unit, legacy := filepath.Join(dir, "user.service"), filepath.Join(dir, "legacy.service")
	t.Setenv("UP_UNIT_PATH", unit)
	t.Setenv("UP_LEGACY_UNIT_PATH", legacy)
	for _, path := range []string{unit, legacy} {
		if err := os.WriteFile(path, []byte("unit"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := install(t, "file://"+dir, dir, "--uninstall")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
	for _, path := range []string{unit, legacy} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unit remains: %s", path)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"systemctl --user disable --now up", "systemctl --user daemon-reload", "systemctl disable --now up", "systemctl daemon-reload"} {
		if !strings.Contains(string(calls), want+"\n") {
			t.Errorf("missing %q: %s", want, calls)
		}
	}
	for _, want := range []string{"Kept ~/.config/up", "Kept /var/lib/up", "up user", "sudo cp /var/lib/up/up.db ~/.config/up/up.db && sudo chown $USER ~/.config/up/up.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	before := string(calls)
	code, _, errOut = install(t, "file://"+dir, dir, "--uninstall")
	after, _ := os.ReadFile(log)
	if code != 0 || errOut != "" || string(after) != before {
		t.Fatalf("missing units invoked commands: %d %q %s", code, errOut, after)
	}
}

func TestInstallUserService(t *testing.T) {
	for _, lingerFails := range []bool{false, true} {
		t.Run(fmt.Sprint(lingerFails), func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			t.Setenv("CALL_LOG", log)
			t.Setenv("USER", "installer")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			unit := filepath.Join(dir, "systemd", "user", "up.service")
			t.Setenv("UP_UNIT_PATH", unit)
			lingerExit := "0"
			if lingerFails {
				lingerExit = "1"
			}
			for name, body := range map[string]string{
				"id":        "#!/bin/sh\necho 1000\n",
				"sudo":      "#!/bin/sh\nprintf 'sudo %s\\n' \"$*\" >>\"$CALL_LOG\"\nexec \"$@\"\n",
				"systemctl": "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
				"loginctl":  "#!/bin/sh\nprintf 'loginctl %s\\n' \"$*\" >>\"$CALL_LOG\"\nexit " + lingerExit + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			base := release(t, fakeBinary, goodSum)
			content, err := os.ReadFile("up.service")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(strings.TrimPrefix(base, "file://"), "up.service"), content, 0644); err != nil {
				t.Fatal(err)
			}
			for _, existing := range []bool{false, true} {
				if existing {
					if err := os.WriteFile(unit, []byte("custom unit"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				code, out, errOut := install(t, base, dir, "--service")
				if code != 0 || errOut != "" {
					t.Fatalf("got %d %q %q", code, out, errOut)
				}
				got, err := os.ReadFile(unit)
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					if string(got) != "custom unit" {
						t.Fatal("replaced existing unit")
					}
				} else if !strings.Contains(string(got), "ExecStart="+dir+"/up serve\n") {
					t.Fatalf("unit: %s", got)
				}
				for _, want := range []string{"Service up is running", "journalctl --user -u up -f", "~/.config/up/up.db", "up tui", "no flags"} {
					if !strings.Contains(out, want) {
						t.Errorf("missing %q: %s", want, out)
					}
				}
				if lingerFails && !strings.Contains(out, "sudo loginctl enable-linger installer") {
					t.Fatal("no linger hint")
				}
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := "systemctl --user daemon-reload\nsystemctl --user enable up\nsystemctl --user restart up\nsudo loginctl enable-linger installer\nloginctl enable-linger installer\n"
			if string(calls) != want+want {
				t.Fatalf("calls: %s", calls)
			}
		})
	}
}

func TestServiceRefusesRootBeforeDownload(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
	dir := t.TempDir()
	code, out, errOut := install(t, "file://"+dir, dir, "--service")
	if code != 1 || out != "" || !strings.Contains(errOut, "user services belong to a normal user") {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("root service installed files")
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

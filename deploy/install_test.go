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
	"syscall"
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
	shell := os.Getenv("UP_TEST_SHELL")
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.Command(shell, append([]string{"../install.sh"}, args...)...)
	unit := os.Getenv("UP_UNIT_PATH")
	if unit == "" {
		unit = filepath.Join(t.TempDir(), "up.service")
	}
	legacy := os.Getenv("UP_LEGACY_UNIT_PATH")
	if legacy == "" {
		legacy = filepath.Join(t.TempDir(), "legacy.service")
	}
	system := os.Getenv("UP_SYSTEM_UNIT_PATH")
	if system == "" {
		system = legacy
	}
	state := os.Getenv("UP_STATE_DIR")
	if state == "" {
		state = filepath.Join(t.TempDir(), "state")
	}
	cmd.Env = append(os.Environ(),
		"UP_STATE_DIR="+state,
		"UP_SYSTEM_UNIT_PATH="+system,
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
	if strings.ContainsAny(out.String()+errOut.String(), "\x1b✓") {
		t.Errorf("non-TTY output contains ANSI escapes or Unicode checkmarks: %q %q", out.String(), errOut.String())
	}
	return code, out.String(), errOut.String()
}

func TestInstallSystemService(t *testing.T) {
	for _, scenario := range []string{"new", "existing user", "root", "existing unit", "existing unit with umask", "database", "database with sidecars", "sidecars without database"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			t.Setenv("CALL_LOG", log)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", dir)
			t.Setenv("TARGET_HOME", dir)
			t.Setenv("SUDO_USER", "installer")
			state := filepath.Join(dir, "state")
			t.Setenv("UP_STATE_DIR", state)
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			var dbFiles []string
			if strings.HasPrefix(scenario, "database") {
				dbFiles = append(dbFiles, "up.db")
			}
			if strings.Contains(scenario, "sidecars") {
				dbFiles = append(dbFiles, "up.db-wal", "up.db-shm")
			}
			for _, name := range dbFiles {
				if err := os.WriteFile(filepath.Join(state, name), []byte("shared data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			uid, exists := "1000", "1"
			if scenario == "root" {
				uid = "0"
				t.Setenv("SUDO_USER", "")
			}
			if scenario == "existing user" {
				exists = "0"
			}
			for name, body := range map[string]string{
				"id":        "case \"$1\" in -u) echo " + uid + ";; -un) echo installer;; up) exit " + exists + ";; *) exit 99;; esac\n",
				"systemctl": "printf 'systemctl %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
				"sudo":      "printf 'sudo %s\\n' \"$*\" >>\"$CALL_LOG\"\nexec \"$@\"\n",
				"useradd":   "printf 'useradd %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
				"usermod":   "printf 'usermod %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
				"getent":    "printf '%s:x:1000:1000::%s:/bin/sh\\n' \"$2\" \"$TARGET_HOME\"\n",
				"loginctl":  "exit 99\n",
				"chmod":     "printf 'chmod %s\\n' \"$*\" >>\"$CALL_LOG\"\n",
				"chown":     "printf 'chown %s\\n' \"$*\" >>\"$CALL_LOG\"\nexit 99\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			unit := filepath.Join(dir, "system", "up.service")
			t.Setenv("UP_SYSTEM_UNIT_PATH", unit)
			userUnit := filepath.Join(dir, "user.service")
			t.Setenv("UP_UNIT_PATH", userUnit)
			if err := os.WriteFile(userUnit, []byte("user unit"), 0644); err != nil {
				t.Fatal(err)
			}
			oldDB := filepath.Join(dir, ".config", "up", "up.db")
			if err := os.MkdirAll(filepath.Dir(oldDB), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldDB, []byte("human data"), 0600); err != nil {
				t.Fatal(err)
			}
			unitContent := "custom unit"
			if scenario == "existing unit with umask" {
				unitContent += "\nUMask=0007\n"
			}
			if strings.HasPrefix(scenario, "existing unit") {
				if err := os.MkdirAll(filepath.Dir(unit), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unit, []byte(unitContent), 0644); err != nil {
					t.Fatal(err)
				}
			}
			base := release(t, fakeBinary, goodSum)
			content, err := os.ReadFile("up-system.service")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(strings.TrimPrefix(base, "file://"), "up-system.service"), content, 0644); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := install(t, base, dir, "--system")
			if code != 0 || errOut != "" {
				t.Fatalf("got %d %q %q", code, out, errOut)
			}
			got, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			warning := "! The existing unit does not set UMask=0007, so group members cannot write the shared database. To replace it, run:\n"
			replace := "  sudo rm " + unit + " && curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --system\n"
			if scenario == "existing unit" {
				if !strings.Contains(out, warning+replace) {
					t.Errorf("missing exact warning and replacement command: %s", out)
				}
			} else if strings.Contains(out, "The existing unit does not set UMask") || strings.Contains(out, "sudo rm ") {
				t.Errorf("unexpected replacement warning: %s", out)
			}
			if strings.HasPrefix(scenario, "existing unit") {
				if string(got) != unitContent || !strings.Contains(out, "Kept existing "+unit+" (delete it and rerun to replace it)") {
					t.Fatalf("replaced unit: %s %s", got, out)
				}
			} else if !strings.Contains(string(got), "ExecStart="+dir+"/up serve --db /var/lib/up/up.db") {
				t.Fatalf("unit: %s", got)
			}
			info, err := os.Stat(unit)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatalf("unit mode: %v %v", info, err)
			}
			for _, want := range []string{"+ Service started", "\nNext steps\n", "journalctl -u up -f", "Database  /var/lib/up/up.db", "1. newgrp up", "2. up tui", "or log out and back in", "Until then: sudo -u up up tui", "system database now takes precedence for up tui"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			wantNotices := 1
			if scenario == "root" {
				wantNotices = 0
			}
			if got := strings.Count(out, "> Using sudo for:"); got != wantNotices {
				t.Errorf("sudo notices: got %d, want %d: %s", got, wantNotices, out)
			}
			if scenario == "new" {
				t.Logf("system output:\n%s", out)
			}
			if scenario != "root" {
				want := "sudo cp '" + oldDB + "' /var/lib/up/up.db && sudo chown up:up /var/lib/up/up.db && sudo chmod 660 /var/lib/up/up.db && sudo systemctl restart up"
				if !strings.Contains(out, want) {
					t.Errorf("missing copy hint %q: %s", want, out)
				}
			}
			data, _ := os.ReadFile(oldDB)
			if string(data) != "human data" {
				t.Fatal("changed source database")
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"systemctl daemon-reload\n", "systemctl enable up\n", "systemctl restart up\n"} {
				if !strings.Contains(string(calls), want) {
					t.Errorf("missing %q: %s", want, calls)
				}
			}
			var chmodCalls []string
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.HasPrefix(line, "chmod ") {
					chmodCalls = append(chmodCalls, line)
				}
			}
			var wantChmod []string
			wantAdjusted := 0
			if strings.HasPrefix(scenario, "database") {
				wantAdjusted = 1
				for _, name := range dbFiles {
					wantChmod = append(wantChmod, "chmod 660 "+filepath.Join(state, name))
				}
				wantChmod = append(wantChmod, "chmod 2770 "+state)
				lastChmod := strings.LastIndex(string(calls), "chmod ")
				if lastChmod > strings.Index(string(calls), "systemctl restart up\n") {
					t.Errorf("permissions adjusted after restart: %s", calls)
				}
			}
			if strings.Join(chmodCalls, "\n") != strings.Join(wantChmod, "\n") {
				t.Errorf("chmod calls: got %q, want %q", chmodCalls, wantChmod)
			}
			if strings.Count(out, "+ Adjusted permissions on the shared database\n") != wantAdjusted {
				t.Errorf("permission notice: %s", out)
			}
			if strings.Contains(string(calls), "chown ") {
				t.Errorf("unexpected ownership change: %s", calls)
			}
			for _, name := range dbFiles {
				data, err := os.ReadFile(filepath.Join(state, name))
				if err != nil || string(data) != "shared data" {
					t.Errorf("changed shared database %s: %q %v", name, data, err)
				}
			}
			adds := strings.Count(string(calls), "\nuseradd ")
			if strings.HasPrefix(string(calls), "useradd ") {
				adds++
			}
			wantAdds := 1
			if scenario == "existing user" {
				wantAdds = 0
			}
			if adds != wantAdds {
				t.Fatalf("useradd count %d: %s", adds, calls)
			}
			if scenario == "root" {
				if strings.Contains(string(calls), "usermod") || !strings.Contains(out, "Skipped group membership") {
					t.Fatalf("root membership: %s %s", calls, out)
				}
			} else if !strings.Contains(string(calls), "usermod -aG up installer\n") {
				t.Fatalf("membership: %s", calls)
			}
		})
	}
}

func TestSharedDirectoryInheritsGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux setgid directories")
	}
	dir := t.TempDir()
	groups, err := os.Getgroups()
	if err != nil || len(groups) == 0 {
		t.Skipf("no available groups: %v", err)
	}
	gid := os.Getegid()
	for _, candidate := range groups {
		if candidate != os.Getegid() {
			gid = candidate
			break
		}
	}
	if err := os.Chown(dir, -1, gid); err != nil {
		t.Skipf("cannot set directory group: %v", err)
	}
	if err := os.Chmod(dir, os.ModeSetgid|0770); err != nil {
		t.Skipf("cannot set setgid: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode()&os.ModeSetgid == 0 {
		t.Skipf("filesystem does not support setgid: %v", err)
	}
	path := filepath.Join(dir, "child")
	if out, err := exec.Command("sh", "-c", ": >\"$1\"", "sh", path).CombinedOutput(); err != nil {
		t.Fatalf("child create: %v %s", err, out)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("group stat unavailable")
	}
	if int(stat.Gid) != gid {
		t.Fatalf("child group %d, want directory group %d", stat.Gid, gid)
	}
	t.Logf("child inherited group %d (process primary group %d)", gid, os.Getegid())
}

func TestInstallServiceModesMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := install(t, "file://"+dir, dir, "--system", "--service")
	if code != 1 || out != "" || errOut != "x install.sh: --system and --service are mutually exclusive\n" {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("rejected options created files")
	}
}

func TestInstallPlacesVerifiedBinary(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := install(t, release(t, fakeBinary, goodSum), dir)
	if code != 0 || errOut != "" || !strings.Contains(out, "Installed "+dir+"/up") {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	for _, want := range []string{"> Installing up (latest, linux/" + runtime.GOARCH + ") to " + dir, "+ Downloaded up_linux_" + runtime.GOARCH + ".tar.gz", "+ Verified checksum", "\nNext steps\n", "1. up serve", "2. up tui", "--service", "--system"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Using sudo") {
		t.Errorf("writable plain install should not use sudo: %s", out)
	}
	t.Logf("plain output:\n%s", out)
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
	base := release(t, fakeBinary, bad)
	code, out, errOut := install(t, base, dir)
	wantOut := fmt.Sprintf("> Installing up (latest, linux/%s) to %s\n  Source: %s\n+ Downloaded up_linux_%s.tar.gz\n", runtime.GOARCH, dir, base, runtime.GOARCH)
	if out != wantOut {
		t.Errorf("stdout: got %q, want %q", out, wantOut)
	}
	want := "x install.sh: checksum mismatch for up_linux_" + runtime.GOARCH + ".tar.gz; nothing was installed\n"
	if code != 1 || errOut != want {
		t.Fatalf("got %d, %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "up")); !os.IsNotExist(err) {
		t.Fatal("binary installed despite checksum mismatch")
	}
}

func TestInstallFailsCleanlyWhenReleaseIsMissing(t *testing.T) {
	dir := t.TempDir()
	base := "file://" + t.TempDir()
	code, out, errOut := install(t, base, dir)
	wantOut := fmt.Sprintf("> Installing up (latest, linux/%s) to %s\n  Source: %s\n", runtime.GOARCH, dir, base)
	if out != wantOut {
		t.Errorf("stdout: got %q, want %q", out, wantOut)
	}
	want := "x install.sh: could not download " + base + "/up_linux_" + runtime.GOARCH + ".tar.gz\n"
	if code != 1 || errOut != want {
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
	for _, want := range []string{"--service", "--system", "--version", "--uninstall", "UP_INSTALL_DIR", "UP_SYSTEM_UNIT_PATH", "UP_STATE_DIR"} {
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
		"id":        "#!/bin/sh\necho 1000\n",
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
	notice := "> Using sudo for: systemctl disable --now up; you may be asked for your password.\n"
	if strings.Count(out, "> Using sudo for:") != 1 || !strings.Contains(out, notice) {
		t.Errorf("expected one notice before the first (silenced) sudo command: %s", out)
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
	for _, want := range []string{"Kept ~/.config/up", "Kept /var/lib/up", "up user", "up group", "system service", "sudo cp /var/lib/up/up.db ~/.config/up/up.db && sudo chown $USER ~/.config/up/up.db"} {
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
				if strings.Count(out, "> Using sudo for:") != 1 {
					t.Errorf("expected one sudo notice: %s", out)
				}
				for _, want := range []string{"+ Service started", "\nNext steps\n", "1. up tui", "journalctl --user -u up -f", "~/.config/up/up.db"} {
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

func TestInstallPinnedVersionSummary(t *testing.T) {
	dir := t.TempDir()
	base := release(t, fakeBinary, goodSum)
	code, out, errOut := install(t, base, dir, "--version", "v1.2.0")
	want := fmt.Sprintf("> Installing up (v1.2.0, linux/%s) to %s\n  Source: %s\n", runtime.GOARCH, dir, base)
	if code != 0 || errOut != "" || !strings.HasPrefix(out, want) {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
	got, err := exec.Command(filepath.Join(dir, "up")).Output()
	if err != nil || string(got) != "fake up\n" {
		t.Fatalf("installed binary ran as %q, %v", got, err)
	}
}

func TestInstallUnexpectedFailurePreservesExitCode(t *testing.T) {
	dir := t.TempDir()
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "install"), []byte("#!/bin/sh\nexit 23\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	base := release(t, fakeBinary, goodSum)
	code, out, errOut := install(t, base, dir)
	wantOut := fmt.Sprintf("> Installing up (latest, linux/%s) to %s\n  Source: %s\n+ Downloaded up_linux_%s.tar.gz\n+ Verified checksum\n", runtime.GOARCH, dir, base, runtime.GOARCH)
	wantErr := "x install.sh failed. Nothing was left half-installed except what is listed above.\n"
	if code != 23 || out != wantOut || errOut != wantErr {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("failed install changed destination: %v", entries)
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

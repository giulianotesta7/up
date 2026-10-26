package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseValidConfig(t *testing.T) {
	data := `
checks:
  - name: api
    url: https://api.example.com/health
    interval: 30s
    timeout: 5s
    expected_status: 200
  - name: website
    url: http://example.com
    interval: 1m
    timeout: 10s
    expected_status: 204
`
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := []Check{
		{Name: "api", URL: "https://api.example.com/health", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200},
		{Name: "website", URL: "http://example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204},
	}
	if len(cfg.Checks) != len(want) {
		t.Fatalf("got %d checks, want %d", len(cfg.Checks), len(want))
	}
	for i := range want {
		if cfg.Checks[i] != want[i] {
			t.Errorf("checks[%d] = %+v, want %+v", i, cfg.Checks[i], want[i])
		}
	}
}

func TestParseRejectsInvalidConfig(t *testing.T) {
	valid := "    url: https://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n"

	tests := []struct {
		name string
		data string
		want string
	}{
		{"empty file", "", "config: at least one check is required"},
		{"no checks", "checks: []\n", "config: at least one check is required"},
		{"malformed yaml", "checks: [\n", "config: invalid YAML"},
		{"unknown field", "checks:\n  - name: api\n" + valid + "    expected_staus: 200\n", "field expected_staus not found"},
		{"missing name", "checks:\n  - name: \"\"\n" + valid, "config: checks[0]: name is required"},
		{"duplicate name", "checks:\n  - name: api\n" + valid + "  - name: api\n" + valid, `config: checks[1] (api): name "api" is already used by checks[0]`},
		{"missing url", "checks:\n  - name: api\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n", "config: checks[0] (api): url is required"},
		{"non-http url", "checks:\n  - name: api\n    url: ftp://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n", `config: checks[0] (api): url "ftp://example.com" must use http or https`},
		{"url without host", "checks:\n  - name: api\n    url: https://\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n", `config: checks[0] (api): url "https://" must include a host`},
		{"missing interval", "checks:\n  - name: api\n    url: https://example.com\n    timeout: 5s\n    expected_status: 200\n", "config: checks[0] (api): interval is required"},
		{"bad interval", "checks:\n  - name: api\n    url: https://example.com\n    interval: often\n    timeout: 5s\n    expected_status: 200\n", `config: checks[0] (api): interval "often" is not a duration (use values like 30s or 1m)`},
		{"zero interval", "checks:\n  - name: api\n    url: https://example.com\n    interval: 0s\n    timeout: 5s\n    expected_status: 200\n", "config: checks[0] (api): interval must be greater than 0"},
		{"missing timeout", "checks:\n  - name: api\n    url: https://example.com\n    interval: 30s\n    expected_status: 200\n", "config: checks[0] (api): timeout is required"},
		{"negative timeout", "checks:\n  - name: api\n    url: https://example.com\n    interval: 30s\n    timeout: -1s\n    expected_status: 200\n", "config: checks[0] (api): timeout must be greater than 0"},
		{"timeout above interval", "checks:\n  - name: api\n    url: https://example.com\n    interval: 5s\n    timeout: 10s\n    expected_status: 200\n", "config: checks[0] (api): timeout (10s) must not exceed interval (5s)"},
		{"missing expected status", "checks:\n  - name: api\n    url: https://example.com\n    interval: 30s\n    timeout: 5s\n", "config: checks[0] (api): expected_status is required"},
		{"out of range expected status", "checks:\n  - name: api\n    url: https://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 600\n", "config: checks[0] (api): expected_status 600 must be between 100 and 599"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.data))
			if err == nil {
				t.Fatalf("Parse() = %+v, want error containing %q", cfg, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	data := "checks:\n  - name: api\n    url: ftp://x\n    interval: 0s\n    timeout: 5s\n    expected_status: 200\n"

	_, err := Parse([]byte(data))
	if err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
	for _, want := range []string{"must use http or https", "interval must be greater than 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Parse() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "up.yaml")
	data := "checks:\n  - name: api\n    url: https://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Checks) != 1 || cfg.Checks[0].Name != "api" {
		t.Fatalf("Load() = %+v, want one check named api", cfg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if want := "config: read " + path; !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %q, want it to contain %q", err, want)
	}
}

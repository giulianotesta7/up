package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

func sealed(b byte) secret.Sealed { return secret.Sealed{Key: []byte{9}, Data: []byte{b}} }

func httpCheck() config.Check {
	return config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200}
}

func tcpCheck() config.Check {
	return config.Check{Name: "db", Kind: config.CheckTCP, Host: "db.example.com", Port: 5432, Interval: 30 * time.Second, Timeout: 5 * time.Second}
}

func TestValidateCheckNewFields(t *testing.T) {
	tests := []struct {
		name   string
		base   func() config.Check
		change func(*config.Check)
		want   string
	}{
		{"http default kind", httpCheck, func(*config.Check) {}, ""},
		{"http explicit kind", httpCheck, func(c *config.Check) { c.Kind = config.CheckHTTP }, ""},
		{"unknown kind", httpCheck, func(c *config.Check) { c.Kind = "icmp" }, `kind "icmp" must be http, tcp, heartbeat, ping or dns`},
		{"every method", httpCheck, func(c *config.Check) { c.Method = "OPTIONS" }, ""},
		{"lowercase method", httpCheck, func(c *config.Check) { c.Method = "get" }, `method "get" must be one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS`},
		{"unknown method", httpCheck, func(c *config.Check) { c.Method = "TRACE" }, `method "TRACE" must be one of`},
		{"header ok", httpCheck, func(c *config.Check) {
			c.Headers = []config.Header{{Name: "X-Api-Key", Value: sealed(1), Edit: config.EditReplace}}
		}, ""},
		{"header empty name", httpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: ""}} }, "header name is required"},
		{"header bad name", httpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: "Bad Name"}} }, `header name "Bad Name" is not valid`},
		{"header colon", httpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: "A:B"}} }, `header name "A:B" is not valid`},
		{"too many headers", httpCheck, func(c *config.Check) {
			for range config.MaxHeaders + 1 {
				c.Headers = append(c.Headers, config.Header{Name: "X"})
			}
		}, "at most 32 headers"},
		{"replace needs value", httpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: "X", Edit: config.EditReplace}} }, `header "X" replace needs a value`},
		{"bad header edit", httpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: "X", Edit: 7}} }, `header "X" has an unknown edit`},
		{"body replace", httpCheck, func(c *config.Check) { c.Method = "POST"; c.Body = sealed(1); c.BodyEdit = config.EditReplace }, ""},
		{"body replace empty", httpCheck, func(c *config.Check) { c.BodyEdit = config.EditReplace }, "body replace needs a value"},
		{"body edit unknown", httpCheck, func(c *config.Check) { c.BodyEdit = 5 }, "body has an unknown edit"},
		{"body too large", httpCheck, func(c *config.Check) {
			c.Body = secret.Sealed{Key: []byte{1}, Data: make([]byte, config.MaxSealedBody+1)}
		}, "body is too large"},
		{"expect text too long", httpCheck, func(c *config.Check) { c.ExpectText = strings.Repeat("a", config.MaxExpectText+1) }, "expected text must be at most 1024 bytes"},
		{"http with host", httpCheck, func(c *config.Check) { c.Host = "x" }, "host and port are only for tcp checks"},
		{"http with port", httpCheck, func(c *config.Check) { c.Port = 80 }, "host and port are only for tcp checks"},
		{"tcp valid", tcpCheck, func(*config.Check) {}, ""},
		{"tcp ipv6", tcpCheck, func(c *config.Check) { c.Host = "2001:db8::1" }, ""},
		{"tcp ipv4", tcpCheck, func(c *config.Check) { c.Host = "192.0.2.1" }, ""},
		{"tcp host missing", tcpCheck, func(c *config.Check) { c.Host = "" }, "host is required"},
		{"tcp host scheme", tcpCheck, func(c *config.Check) { c.Host = "tcp://db" }, `host "tcp://db" must be a host name or IP address`},
		{"tcp host brackets", tcpCheck, func(c *config.Check) { c.Host = "[::1]" }, `host "[::1]" must be a host name or IP address`},
		{"tcp host colon text", tcpCheck, func(c *config.Check) { c.Host = "db:5432" }, `host "db:5432" must be a host name or IP address`},
		{"tcp port zero", tcpCheck, func(c *config.Check) { c.Port = 0 }, "port 0 must be between 1 and 65535"},
		{"tcp port high", tcpCheck, func(c *config.Check) { c.Port = 65536 }, "port 65536 must be between 1 and 65535"},
		{"tcp port bounds", tcpCheck, func(c *config.Check) { c.Port = 65535 }, ""},
		{"tcp with url", tcpCheck, func(c *config.Check) { c.URL = "https://example.com" }, "url is only for http checks"},
		{"tcp with status", tcpCheck, func(c *config.Check) { c.ExpectedStatus = 200 }, "expected status is only for http checks"},
		{"tcp with method", tcpCheck, func(c *config.Check) { c.Method = "POST" }, "method, headers, body and expected text are only for http checks"},
		{"tcp with header", tcpCheck, func(c *config.Check) { c.Headers = []config.Header{{Name: "X"}} }, "method, headers, body and expected text are only for http checks"},
		{"tcp with expect", tcpCheck, func(c *config.Check) { c.ExpectText = "ok" }, "method, headers, body and expected text are only for http checks"},
		{"tcp name", tcpCheck, func(c *config.Check) { c.Name = "" }, "name is required"},
		{"tcp timeout above interval", tcpCheck, func(c *config.Check) { c.Timeout = time.Minute }, "must not exceed interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.base()
			tt.change(&c)
			err := config.ValidateCheck(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCanonicalAndSameDefinition(t *testing.T) {
	base := httpCheck()
	base.Method, base.Headers, base.Body, base.ExpectText = "POST", []config.Header{{Name: "A", Value: sealed(1)}}, sealed(2), "ok"
	if !base.SameDefinition(base) {
		t.Fatal("a definition differs from itself")
	}
	// Equivalent spellings are the same definition.
	alt := base
	alt.Kind = config.CheckHTTP
	alt.Headers = []config.Header{{Name: "A", Value: sealed(1), Edit: config.EditKeep}}
	if !base.SameDefinition(alt) {
		t.Fatal("kind http and empty are different")
	}
	get, getExplicit := httpCheck(), httpCheck()
	getExplicit.Method = "GET"
	getExplicit.Headers = []config.Header{}
	if !get.SameDefinition(getExplicit) {
		t.Fatal("empty and GET methods, nil and empty headers are different")
	}
	if c := getExplicit.Canonical(); c.Method != "" || c.Headers != nil || c.Kind != "" {
		t.Fatalf("Canonical() = %+v", c)
	}
	// Every executable or alert-relevant field changes the identity.
	changes := map[string]func(*config.Check){
		"name":          func(c *config.Check) { c.Name = "other" },
		"url":           func(c *config.Check) { c.URL = "https://other.example.com" },
		"interval":      func(c *config.Check) { c.Interval = time.Minute },
		"timeout":       func(c *config.Check) { c.Timeout = time.Second },
		"status":        func(c *config.Check) { c.ExpectedStatus = 204 },
		"channel":       func(c *config.Check) { c.Channel = "ops" },
		"alert after":   func(c *config.Check) { c.AlertAfter = 3 },
		"recover after": func(c *config.Check) { c.RecoverAfter = 3 },
		"tls off":       func(c *config.Check) { c.TLSOff = true },
		"kind":          func(c *config.Check) { c.Kind = config.CheckTCP },
		"method":        func(c *config.Check) { c.Method = "PUT" },
		"header name":   func(c *config.Check) { c.Headers = []config.Header{{Name: "B", Value: sealed(1)}} },
		"header value":  func(c *config.Check) { c.Headers = []config.Header{{Name: "A", Value: sealed(3)}} },
		"header added":  func(c *config.Check) { c.Headers = append(slicesClone(c.Headers), config.Header{Name: "C"}) },
		"header order": func(c *config.Check) {
			c.Headers = []config.Header{{Name: "B"}, {Name: "A", Value: sealed(1)}}
		},
		"body":        func(c *config.Check) { c.Body = sealed(4) },
		"body key":    func(c *config.Check) { c.Body = secret.Sealed{Key: []byte{8}, Data: []byte{2}} },
		"expect text": func(c *config.Check) { c.ExpectText = "fine" },
		"host":        func(c *config.Check) { c.Host = "h" },
		"port":        func(c *config.Check) { c.Port = 22 },
	}
	for name, change := range changes {
		other := base
		other.Headers = slicesClone(base.Headers)
		change(&other)
		if base.SameDefinition(other) || other.SameDefinition(base) {
			t.Errorf("changing %s kept the same definition", name)
		}
	}
	// Edit intents are not part of identity: only stored values are.
	edited := base
	edited.Headers = []config.Header{{Name: "A", Value: sealed(1), Edit: config.EditKeep}}
	edited.BodyEdit = config.EditKeep
	if !base.SameDefinition(edited) {
		t.Fatal("edit intent changed identity")
	}
	canon := base.Canonical()
	canon.Headers[0].Name = "mutated"
	if base.Headers[0].Name != "A" {
		t.Fatal("Canonical aliases the header slice")
	}
	if !reflect.DeepEqual(base.Canonical(), base.Canonical().Canonical()) {
		t.Fatal("Canonical is not idempotent")
	}
}

func slicesClone(h []config.Header) []config.Header { return append([]config.Header(nil), h...) }

func TestEditNames(t *testing.T) {
	for name, want := range map[string]config.Edit{"": config.EditKeep, "keep": config.EditKeep, "replace": config.EditReplace, "clear": config.EditClear} {
		got, err := config.ParseEdit(name)
		if err != nil || got != want {
			t.Errorf("ParseEdit(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := config.ParseEdit("drop"); err == nil || err.Error() != `edit "drop" must be keep, replace or clear` {
		t.Fatalf("ParseEdit(drop) = %v", err)
	}
	if config.EditKeep.String() != "keep" || config.EditReplace.String() != "replace" || config.EditClear.String() != "clear" {
		t.Fatal("Edit.String")
	}
}

package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func TestNormalizeTagsLowercasesKeepsOrderAndDropsRepeats(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{"NAS"}, []string{"nas"}},
		{[]string{" nas ", "Home", "nas", "HOME", "", "prod-1"}, []string{"nas", "home", "prod-1"}},
		{[]string{"home", "nas"}, []string{"home", "nas"}},
	}
	for _, tt := range tests {
		if got := config.NormalizeTags(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("NormalizeTags(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseAndFormatTags(t *testing.T) {
	if got := config.ParseTags("NAS, home,, nas ,prod"); !reflect.DeepEqual(got, []string{"nas", "home", "prod"}) {
		t.Fatalf("ParseTags = %q", got)
	}
	if got := config.ParseTags("  "); got != nil {
		t.Fatalf("ParseTags(blank) = %q, want nil", got)
	}
	if got := config.FormatTags([]string{"nas", "home"}); got != "nas, home" {
		t.Fatalf("FormatTags = %q", got)
	}
}

func TestValidateTag(t *testing.T) {
	tests := []struct{ tag, want string }{
		{"nas", ""},
		{"NAS", ""},
		{"prod-1", ""},
		{"", "tag is required"},
		{"na s", `tag "na s" may only use letters a-z, digits and -`},
		{"n_s", `tag "n_s" may only use letters a-z, digits and -`},
		{"café", `tag "café" may only use letters a-z, digits and -`},
		{"#nas", `tag "#nas" may only use letters a-z, digits and -`},
		{strings.Repeat("a", config.MaxTagLength), ""},
		{strings.Repeat("a", config.MaxTagLength+1), "tag \"" + strings.Repeat("a", config.MaxTagLength+1) + "\" must be at most 32 characters"},
	}
	for _, tt := range tests {
		err := config.ValidateTag(tt.tag)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || err.Error() != tt.want) {
			t.Errorf("ValidateTag(%q) = %v, want %q", tt.tag, err, tt.want)
		}
	}
}

func TestValidateCheckTags(t *testing.T) {
	http := config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200}
	tcp := config.Check{Name: "db", Kind: config.CheckTCP, Host: "db.local", Port: 5432, Interval: 30 * time.Second, Timeout: 5 * time.Second}
	beat := config.Check{Name: "job", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: time.Minute}
	for _, c := range []config.Check{http, tcp, beat} {
		c.Tags = []string{"NAS", "home", "nas", "a", "b", "c"}
		if err := config.ValidateCheck(c); err != nil {
			t.Fatalf("%s: five distinct tags after normalization: %v", c.Name, err)
		}
		c.Tags = []string{"a", "b", "c", "d", "e", "f"}
		if err := config.ValidateCheck(c); err == nil || !strings.Contains(err.Error(), "at most 5 tags are allowed") {
			t.Fatalf("%s: six tags = %v", c.Name, err)
		}
		c.Tags = []string{"ok", "bad tag"}
		if err := config.ValidateCheck(c); err == nil || !strings.Contains(err.Error(), `tag "bad tag" may only use letters a-z, digits and -`) {
			t.Fatalf("%s: invalid tag = %v", c.Name, err)
		}
	}
}

func TestCanonicalNormalizesTags(t *testing.T) {
	c := config.Check{Name: "api", Tags: []string{"NAS", "home", "nas"}}
	got := c.Canonical()
	if !reflect.DeepEqual(got.Tags, []string{"nas", "home"}) {
		t.Fatalf("Canonical().Tags = %q", got.Tags)
	}
	if !reflect.DeepEqual(c.Tags, []string{"NAS", "home", "nas"}) {
		t.Fatalf("Canonical changed its input: %q", c.Tags)
	}
	if (config.Check{Tags: []string{}}).Canonical().Tags != nil {
		t.Fatal("an empty tag list is not nil once canonical")
	}
	if !got.HasTag("nas") || got.HasTag("prod") {
		t.Fatalf("HasTag on %q", got.Tags)
	}
}

func TestTagsInUseAreSortedAndDistinct(t *testing.T) {
	checks := []config.Check{{Name: "a", Tags: []string{"prod", "nas"}}, {Name: "b"}, {Name: "c", Tags: []string{"home", "nas"}}}
	if got := config.TagsInUse(checks); !reflect.DeepEqual(got, []string{"home", "nas", "prod"}) {
		t.Fatalf("TagsInUse = %q", got)
	}
	if got := config.TagsInUse(nil); got != nil {
		t.Fatalf("TagsInUse(nil) = %q", got)
	}
}

func TestMaintenanceWindowForATag(t *testing.T) {
	w := config.MaintenanceWindow{Tag: "nas", Weekdays: []time.Weekday{1}, StartMinute: 60, Duration: time.Hour, Zone: "UTC"}
	if err := config.ValidateMaintenanceWindow(w); err != nil {
		t.Fatalf("tag window: %v", err)
	}
	w.Tag = "bad tag"
	if err := config.ValidateMaintenanceWindow(w); err == nil || !strings.Contains(err.Error(), `tag "bad tag" may only use letters a-z, digits and -`) {
		t.Fatalf("invalid tag window = %v", err)
	}
	w.Tag = ""
	if err := config.ValidateMaintenanceWindow(w); err == nil || !strings.Contains(err.Error(), "check is required") {
		t.Fatalf("window without check or tag = %v", err)
	}
}

// Tags do not change how a monitor runs, so a tag edit keeps its loop.
func TestSameDefinitionIgnoresTags(t *testing.T) {
	a := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}
	b := a
	b.Tags = []string{"nas"}
	if !a.SameDefinition(b) {
		t.Fatal("a tag edit changed the definition")
	}
}

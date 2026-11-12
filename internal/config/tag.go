package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Bounds on a monitor's tags.
const (
	MaxTags = 5
	// MaxTagLength is the longest tag in characters.
	MaxTagLength = 32
)

// NormalizeTags returns tags in their stored form, without changing tags:
// trimmed and lowercased ("NAS" is "nas"), in the order typed, empty entries
// and repeats dropped. An empty result is nil. It does not validate them; see
// ValidateTag.
func NormalizeTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// ParseTags reads the form's comma-separated list ("nas, home") into
// normalized tags.
func ParseTags(text string) []string { return NormalizeTags(strings.Split(text, ",")) }

// FormatTags writes tags the way the form shows them: "nas, home".
func FormatTags(tags []string) string { return strings.Join(tags, ", ") }

// ValidateTag checks one tag once normalized: lowercase letters a-z, digits
// and "-", at most MaxTagLength characters.
func ValidateTag(tag string) error {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return errors.New("tag is required")
	}
	for _, r := range tag {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("tag %q may only use letters a-z, digits and -", tag)
		}
	}
	if len(tag) > MaxTagLength {
		return fmt.Errorf("tag %q must be at most %d characters", tag, MaxTagLength)
	}
	return nil
}

// validateTags checks a monitor's tags once normalized: at most MaxTags, each
// valid. Every kind of monitor can have tags.
func validateTags(tags []string) []error {
	tags = NormalizeTags(tags)
	var errs []error
	if len(tags) > MaxTags {
		errs = append(errs, fmt.Errorf("at most %d tags are allowed", MaxTags))
	}
	for _, t := range tags {
		if err := ValidateTag(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// TagsInUse lists every tag of checks once, sorted: what the form offers as
// "in use".
func TagsInUse(checks []Check) []string {
	var out []string
	for _, c := range checks {
		for _, t := range NormalizeTags(c.Tags) {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// HasTag reports whether c carries tag (both compared normalized).
func (c Check) HasTag(tag string) bool {
	return slices.Contains(NormalizeTags(c.Tags), strings.ToLower(strings.TrimSpace(tag)))
}

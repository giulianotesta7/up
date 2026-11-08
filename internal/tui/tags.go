package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

// popupRowWidth is the widest popup row that still fits an 80-column
// terminal: 78 cells of box less the border and padding.
const popupRowWidth = 74

// tagsInUse lists the tags of the snapshot's monitors once, sorted.
func (s Snapshot) tagsInUse() []string {
	checks := make([]config.Check, len(s.Services))
	for i, svc := range s.Services {
		checks[i] = svc.Check
	}
	return config.TagsInUse(checks)
}

// tagMembers names the snapshot's monitors carrying tag, in list order.
func (s Snapshot) tagMembers(tag string) []string {
	var names []string
	for _, svc := range s.Services {
		if svc.Check.HasTag(tag) {
			names = append(names, svc.Check.Name)
		}
	}
	return names
}

// joinNames lists names as "a", "a and b" or "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// membersText names a tag's monitors, or counts them when the names would
// take more than room cells.
func membersText(names []string, tag string, room int) string {
	if text := joinNames(names); ansi.StringWidth(text) <= room {
		return text
	}
	return plural(len(names), "monitor") + " tagged " + tag
}

// coversText says what a mute or window for tag holds: "covers nas-smb,
// nas-web and any tagged later", or "covers 2 monitors and any tagged later"
// when the names do not fit after the window form's Applies to row, the
// wider of the two rows that show it, so both say it the same way.
func coversText(snap Snapshot, tag string) string {
	names := snap.tagMembers(tag)
	room := popupRowWidth - ansi.StringWidth(fmt.Sprintf("> %-12s ‹ #%s ›   ", "Applies to", tag))
	if text := "covers " + strings.Join(names, ", ") + " and any tagged later"; ansi.StringWidth(text) <= room {
		return text
	}
	return "covers " + plural(len(names), "monitor") + " and any tagged later"
}

// hashTags renders tags as "#nas #home".
func hashTags(tags []string) string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = "#" + t
	}
	return strings.Join(out, " ")
}

// shortTags renders tags as "#nas +1": the first one and how many more.
func shortTags(tags []string) string {
	if len(tags) < 2 {
		return hashTags(tags)
	}
	return fmt.Sprintf("#%s +%d", tags[0], len(tags)-1)
}

// tagsLine is the form's Tags row. Focused, it says the format and the tags
// in use, or names a typed tag no monitor has yet so a typo does not start a
// new group unnoticed; unfocused, only that warning.
func tagsLine(f monitorForm, snap Snapshot) string {
	marker, label, value := " ", "Tags", f.tags
	focused := f.focus == focusTags
	if focused {
		runes := []rune(value)
		cursor := min(f.cursor, len(runes))
		value = string(runes[:cursor]) + accentStyle.Render("│") + string(runes[cursor:])
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	line := fmt.Sprintf("%s %s: %s", marker, label, value)
	inUse := snap.tagsInUse()
	var fresh []string
	for _, t := range config.ParseTags(f.tags) {
		if config.ValidateTag(t) == nil && !slices.Contains(inUse, t) {
			fresh = append(fresh, t)
		}
	}
	width := popupRowWidth
	if f.tagsWidth > 0 {
		width = f.tagsWidth
	}
	room := width - ansi.StringWidth(line) - 3
	hint, warning := "", ""
	switch {
	case len(fresh) == 1:
		warning = fresh[0] + " is a new tag"
	case len(fresh) > 1:
		warning = joinNames(fresh) + " are new tags"
	case focused && len(inUse) > 0:
		hint = " · in use: " + strings.Join(inUse, ", ")
	}
	// Names that would not fit the box become a count.
	if prefix := ""; warning != "" {
		if focused {
			prefix = "comma-separated · "
		}
		if ansi.StringWidth(prefix+warning) > room {
			warning = plural(len(fresh), "new tag")
		}
	}
	switch {
	case focused && warning != "":
		hint = helpStyle.Render("comma-separated · ") + warnStyle.Render(warning)
	case focused:
		hint = helpStyle.Render("comma-separated" + hint)
	case warning != "":
		hint = warnStyle.Render(warning)
	default:
		return line + "\n"
	}
	if room < 8 {
		return line + "\n"
	}
	return line + "   " + ansi.Truncate(hint, room, "…") + "\n"
}

// tagColumn is the list's tag cell for each width: full ("#nas #home") or
// short ("#nas +1").
func tagColumn(snap Snapshot) (full, short int) {
	for _, s := range snap.Services {
		full = max(full, ansi.StringWidth(hashTags(s.Check.Tags)))
		short = max(short, ansi.StringWidth(shortTags(s.Check.Tags)))
	}
	return full, short
}

// tagMaintenance names the tag whose window holds s now, when none of its
// own windows does.
func tagMaintenance(s Service, now time.Time) string {
	if !s.InMaintenance.Active {
		return ""
	}
	byOwner := map[string][]config.MaintenanceWindow{}
	for _, w := range s.Maintenance {
		byOwner[w.Tag] = append(byOwner[w.Tag], w)
	}
	if config.EvaluateMaintenance(byOwner[""], now).Active {
		return ""
	}
	for _, tag := range config.NormalizeTags(s.Check.Tags) {
		if config.EvaluateMaintenance(byOwner[tag], now).Active {
			return tag
		}
	}
	return ""
}

// throughTags counts a monitor's windows that come from its tags:
// " · 1 through #nas".
func throughTags(s Service) string {
	var out strings.Builder
	for _, tag := range config.NormalizeTags(s.Check.Tags) {
		n := 0
		for _, w := range s.Maintenance {
			if w.Tag == tag {
				n++
			}
		}
		if n > 0 {
			fmt.Fprintf(&out, " · %d through #%s", n, tag)
		}
	}
	return out.String()
}

package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

// Ping and DNS forms have the Tags row of every kind, after Timeout and
// before Channel; edit and duplicate carry the tags (an edit that dropped
// them would also delete the tag's mute).
func TestPingAndDNSFormsCarryTags(t *testing.T) {
	ping, dns := pingFixture, dnsFixture
	ping.Tags, dns.Tags = []string{"nas", "home"}, []string{"nas"}
	for _, c := range []config.Check{ping, dns} {
		t.Run(c.Kind, func(t *testing.T) {
			s, m := probeModel(t, "", 120, 50, ping, dns)
			ctx := context.Background()
			until := now.Add(time.Hour)
			if err := s.SetTagMute(ctx, "nas", until); err != nil {
				t.Fatal(err)
			}
			m = selectNamed(t, m, c.Name)
			m, _ = press(t, m, keyE)
			requireText(t, m, "Tags: "+config.FormatTags(c.Tags))
			m = focusRow(t, m, "Timeout")
			m, _ = press(t, m, keyTab)
			requireText(t, m, "> Tags: "+config.FormatTags(c.Tags))
			m, _ = press(t, m, keyTab)
			requireText(t, m, "> Channel:")
			m, cmd := press(t, m, keyEnter)
			m = finishCommand(t, m, cmd)
			if got := tagsOf(t, s, c.Name); !reflect.DeepEqual(got, c.Tags) {
				t.Fatalf("edit stored tags %q, want %q", got, c.Tags)
			}
			if mutes, err := s.TagMutes(ctx); err != nil || len(mutes) != 1 || mutes[0].Tag != "nas" || !mutes[0].Until.Equal(until) {
				t.Fatalf("tag mutes after edit = %+v %v", mutes, err)
			}

			m = selectNamed(t, m, c.Name)
			m, _ = press(t, m, keyY)
			requireText(t, m, "Name: "+c.Name+" copy", "Tags: "+config.FormatTags(c.Tags))
			m, cmd = press(t, m, keyEnter)
			finishCommand(t, m, cmd)
			copied, ok := storedByName(t, s, c.Name+" copy")
			if !ok || copied.Kind != c.Kind || !reflect.DeepEqual(copied.Tags, c.Tags) {
				t.Fatalf("copy %+v, %v, want kind %s and tags %q", copied, ok, c.Kind, c.Tags)
			}
		})
	}
}

// A new ping monitor saves the tags typed in its Tags row, normalized.
func TestNewPingMonitorSavesTags(t *testing.T) {
	s, m := probeModel(t, "", 120, 50, dnsFixture)
	m = openKind(t, m, 3)
	requireText(t, m, "Type: ‹ Ping › ")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "gw")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "192.168.1.1")
	m = focusRow(t, m, "Tags")
	m = typeText(t, m, "NAS, home")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	c, ok := storedByName(t, s, "gw")
	if !ok || c.Kind != config.CheckPing || !reflect.DeepEqual(c.Tags, []string{"nas", "home"}) {
		t.Fatalf("stored %+v, %v, want a ping with tags [nas home]", c, ok)
	}
}

// The list shows the kind tag and the monitor tags together, and #tag
// search finds ping and DNS monitors.
func TestPingAndDNSListTagsAndTagSearch(t *testing.T) {
	ping, dns := pingFixture, dnsFixture
	ping.Tags, dns.Tags = []string{"nas"}, []string{"home"}
	other := config.Check{Name: "web", URL: "https://example.com", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200}
	snap := Snapshot{HasBeat: true, LastBeat: now, Services: []Service{{Check: ping}, {Check: dns}, {Check: other}}}
	wide := ansi.Strip(renderList(snap, 0, now, nil, 160))
	for _, want := range []string{"router  ping  #nas   PENDING", "api-dns dns   #home  PENDING", "web                  PENDING"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide list lacks %q:\n%s", want, wide)
		}
	}
	m := modelWith(snap)
	m.width, m.height = 160, 50
	m, _ = press(t, m, keySlash)
	m = typeText(t, m, "#nas")
	requireText(t, m, "1 of 3", "router")
	if got := view(m); strings.Contains(got, "api-dns") || strings.Contains(got, "web") {
		t.Fatalf("#nas should show only router:\n%s", got)
	}
}

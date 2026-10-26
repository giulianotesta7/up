package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

type deletePrompt struct {
	name   string
	saving bool
	err    error
}

type deleteMsg struct {
	generation uint64
	err        error
}

func (m Model) handleDeleteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.deletion
	m.deletion = &p
	if p.saving {
		return m, nil
	}
	switch msg.String() {
	case "n", "esc":
		m.deletion = nil
	case "y":
		p.saving = true
		m.generation++
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			return deleteMsg{generation: m.generation, err: m.store.DeleteCheck(ctx, p.name)}
		}
	}
	return m, nil
}

func renderDelete(p deletePrompt, snap Snapshot, now time.Time, loadErr error) string {
	content := titleStyle.Render("Delete monitor") + "\n" + renderBanners(snap, now, loadErr) + "\n"
	content += fmt.Sprintf("Delete monitor %q and all its history? This cannot be undone.\n", p.name)
	if p.err != nil {
		content += "\n" + warnStyle.Render("Cannot delete monitor: "+p.err.Error()) + "\n"
	}
	if p.saving {
		content += "\n" + helpStyle.Render("Deleting monitor… · ctrl+c quit")
	} else {
		content += "\n" + helpStyle.Render("y delete · n cancel")
	}
	return content
}

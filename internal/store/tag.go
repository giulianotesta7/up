package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/heohlee/up/internal/config"
)

// Tags live in the checks.tags column as their normalized list joined by
// commas ("nas,home"), which a tag cannot contain. A tag's mutes and windows
// name the tag, never its monitors, so membership is resolved on every read
// and a monitor tagged later is covered too.

func encodeTags(tags []string) string { return strings.Join(tags, ",") }

func decodeTags(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, ",")
}

// carriesTag is an SQL condition: the checks row aliased c carries the tag
// given by the expression tag.
func carriesTag(c, tag string) string {
	return "instr(',' || " + c + ".tags || ',', ',' || " + tag + " || ',') > 0"
}

// normalTag normalizes and validates a tag given on its own (a mute or window
// target); an invalid one is ErrValidation.
func normalTag(op, tag string) (string, error) {
	if err := config.ValidateTag(tag); err != nil {
		return "", classify(ErrValidation, fmt.Errorf("store: %s: %w", op, err))
	}
	return strings.ToLower(strings.TrimSpace(tag)), nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// pruneTags deletes the mutes and windows of every tag no monitor carries any
// more, so removing a tag from its last monitor (edit, delete or SetChecks)
// leaves nothing orphaned. It runs in the transaction of that write.
func pruneTags(ctx context.Context, tx execer) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM tag_mutes WHERE NOT EXISTS (SELECT 1 FROM checks c WHERE "+carriesTag("c", "tag_mutes.tag")+")"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM maintenance_windows WHERE check_name = '' AND NOT EXISTS (SELECT 1 FROM checks c WHERE "+carriesTag("c", "maintenance_windows.tag")+")")
	return err
}

// SetTagMute creates or replaces the mute of a tag until the given time. It
// holds every monitor carrying the tag, including monitors tagged later. An
// invalid tag is ErrValidation and a tag no monitor carries ErrNotFound.
func (s *Store) SetTagMute(ctx context.Context, tag string, until time.Time) error {
	tag, err := normalTag("set tag mute", tag)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tag_mutes (tag, until_ns) SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM checks c WHERE `+carriesTag("c", "?1")+`) ON CONFLICT(tag) DO UPDATE SET until_ns = excluded.until_ns`, tag, until.UnixNano())
	if err != nil {
		return fmt.Errorf("store: set tag mute: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set tag mute: %w", err)
	}
	if n == 0 {
		return classify(ErrNotFound, fmt.Errorf("store: set tag mute: no monitor has tag %q", tag))
	}
	return nil
}

// UnmuteTag ends a tag's mute for all its monitors at once. A tag's mute
// keeps no notification state, so it is deleted; the daemon then sends what
// the recipients of each monitor do not know, unless something else holds
// it. A tag without a mute is not an error; an invalid tag is ErrValidation.
func (s *Store) UnmuteTag(ctx context.Context, tag string) error {
	tag, err := normalTag("unmute tag", tag)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM tag_mutes WHERE tag = ?", tag); err != nil {
		return fmt.Errorf("store: unmute tag: %w", err)
	}
	return nil
}

// TagMutes returns every tag's mute ordered by tag, with Tag set and no
// Check. A mute may already be over.
func (s *Store) TagMutes(ctx context.Context) ([]config.Mute, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT tag, until_ns FROM tag_mutes ORDER BY tag")
	if err != nil {
		return nil, fmt.Errorf("store: tag mutes: %w", err)
	}
	defer rows.Close()
	var out []config.Mute
	for rows.Next() {
		var m config.Mute
		var ns int64
		if err := rows.Scan(&m.Tag, &ns); err != nil {
			return nil, fmt.Errorf("store: tag mutes: %w", err)
		}
		m.Until = fromNanos(ns)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: tag mutes: %w", err)
	}
	return out, nil
}

// TagMaintenanceWindows returns a tag's own windows in creation order, with
// Tag set and no Check. An invalid tag is ErrValidation.
func (s *Store) TagMaintenanceWindows(ctx context.Context, tag string) ([]config.MaintenanceWindow, error) {
	tag, err := normalTag("maintenance windows", tag)
	if err != nil {
		return nil, err
	}
	return s.windows(ctx, "SELECT "+windowColumns+" FROM maintenance_windows WHERE check_name = '' AND tag = ? ORDER BY id", tag)
}

// DeleteTagMaintenanceWindow removes window id of a tag; a window of another
// tag or a monitor, or an unknown ID, is ErrNotFound.
func (s *Store) DeleteTagMaintenanceWindow(ctx context.Context, tag string, id int64) error {
	tag, err := normalTag("delete maintenance window", tag)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM maintenance_windows WHERE id = ? AND check_name = '' AND tag = ?", id, tag)
	if err != nil {
		return fmt.Errorf("store: delete maintenance window: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: delete maintenance window: %w", err)
		}
		return classify(ErrNotFound, fmt.Errorf("store: delete maintenance window: window %d of tag %q not found", id, tag))
	}
	return nil
}

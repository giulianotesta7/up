package alert

import (
	"context"
	"errors"

	"github.com/giulianotesta7/up/internal/config"
)

// ErrWebhookUnavailable is returned for webhook channels until the webhook
// transport is implemented.
var ErrWebhookUnavailable = errors.New("webhook delivery is not available yet")

// deliverWebhook is the webhook transport behind Deliver. It logs its own
// failures and returns them, like deliverEmail.
func (n *Notifier) deliverWebhook(_ context.Context, ch config.Channel, e event) error {
	n.opts.Logger.Error("unsupported alert channel kind", "channel", e.check.Channel, "kind", ch.Kind)
	return ErrWebhookUnavailable
}

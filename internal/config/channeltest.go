package config

import "time"

// ChannelTestStatus is the state of a channel test request.
type ChannelTestStatus string

// Channel test statuses. A request moves pending -> sending -> ok|error and
// never back.
const (
	TestPending ChannelTestStatus = "pending"
	TestSending ChannelTestStatus = "sending"
	TestOK      ChannelTestStatus = "ok"
	TestError   ChannelTestStatus = "error"
)

// Active reports whether the request is unfinished. A channel has at most one
// active request.
func (s ChannelTestStatus) Active() bool { return s == TestPending || s == TestSending }

// ChannelTest is one request to send a test message through a channel. The
// TUI (local or remote) enqueues it and the daemon processes it. Error is a
// short, secret-free reason, set only when Status is TestError. Started and
// Finished are zero until they happen.
type ChannelTest struct {
	ID        int64
	Channel   string
	Status    ChannelTestStatus
	Error     string
	Requested time.Time
	Started   time.Time
	Finished  time.Time
}

package check

// Transition is a change in whether an alert is outstanding.
type Transition int

const (
	NoChange  Transition = iota
	WentDown             // enough consecutive failures: send the down alert
	Recovered            // enough consecutive successes: send the recovery alert
)

// Streak tracks consecutive results and whether a down alert is outstanding.
// Every result is stored regardless; Streak only decides when to notify.
type Streak struct {
	// Alerting is true from the down alert until the recovery alert.
	Alerting  bool
	Failures  int
	Successes int
}

// Observe applies one result. Thresholds below 1 count as 1.
func (s *Streak) Observe(status Status, alertAfter, recoverAfter int) Transition {
	alertAfter, recoverAfter = max(alertAfter, 1), max(recoverAfter, 1)
	switch status {
	case StatusDown:
		s.Failures, s.Successes = s.Failures+1, 0
		if !s.Alerting && s.Failures >= alertAfter {
			s.Alerting = true
			return WentDown
		}
	case StatusUp:
		s.Failures, s.Successes = 0, s.Successes+1
		if s.Alerting && s.Successes >= recoverAfter {
			s.Alerting = false
			return Recovered
		}
	}
	return NoChange
}

// Replay rebuilds a Streak from stored results, newest first. It sends
// nothing: it only recovers the state a running daemon would hold.
func Replay(newestFirst []Result, alertAfter, recoverAfter int) Streak {
	var s Streak
	for i := len(newestFirst) - 1; i >= 0; i-- {
		s.Observe(newestFirst[i].Status, alertAfter, recoverAfter)
	}
	return s
}

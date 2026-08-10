package session

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func activeSession() Session {
	return Session{
		ID:        "ses_1",
		TaskID:    "PROJ-001",
		AttemptID: "att_1",
		Actor:     "agent:codex",
		Status:    StatusActive,
		StartedAt: time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
	}
}

func TestFinish(t *testing.T) {
	at := time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		session Session
		summary string
		wantErr error
	}{
		{name: "active session", session: activeSession(), summary: "Implemented it"},
		{name: "summary required", session: activeSession(), wantErr: ErrSummaryRequired},
		{name: "terminal session", session: Session{Status: StatusCanceled}, summary: "done", wantErr: ErrTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Finish(tt.session, tt.summary, "abc123", at)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Finish error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (got.Status != StatusFinished || got.EndedAt == nil || !got.EndedAt.Equal(at) || got.Summary != tt.summary) {
				t.Fatalf("Finish result = %+v", got)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	at := time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC)
	got, err := Cancel(activeSession(), "superseded", at)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.Status != StatusCanceled || got.CancelReason != "superseded" || got.EndedAt == nil || !got.EndedAt.Equal(at) {
		t.Fatalf("Cancel result = %+v", got)
	}
	if _, err := Cancel(activeSession(), "", at); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("empty reason error = %v", err)
	}
}

func TestDeriveHealth(t *testing.T) {
	s := activeSession()
	tests := []struct {
		name string
		s    Session
		live *Live
		now  time.Time
		want Health
	}{
		{name: "fresh start", s: s, now: s.StartedAt.Add(time.Minute), want: HealthActive},
		{name: "stale without heartbeat", s: s, now: s.StartedAt.Add(4 * time.Minute), want: HealthStalled},
		{name: "fresh heartbeat", s: s, live: &Live{HeartbeatAt: s.StartedAt.Add(3 * time.Minute)}, now: s.StartedAt.Add(4 * time.Minute), want: HealthActive},
		{name: "finished", s: Session{Status: StatusFinished}, now: s.StartedAt.Add(time.Hour), want: HealthFinished},
		{name: "canceled", s: Session{Status: StatusCanceled}, now: s.StartedAt.Add(time.Hour), want: HealthCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveHealth(tt.s, tt.live, tt.now, 3*time.Minute); got != tt.want {
				t.Fatalf("DeriveHealth = %q, want %q", got, tt.want)
			}
		})
	}
}

// A zero staleAfter (what a raw Config{} field would yield) makes any elapsed time stale,
// confirming callers must pass config.SessionStaleDuration()'s fallback rather than the
// bare field. Only an exactly-now reading stays active.
func TestDeriveHealthZeroStaleAfter(t *testing.T) {
	s := activeSession()
	if got := DeriveHealth(s, nil, s.StartedAt, 0); got != HealthActive {
		t.Fatalf("DeriveHealth at start = %q, want active", got)
	}
	if got := DeriveHealth(s, nil, s.StartedAt.Add(time.Nanosecond), 0); got != HealthStalled {
		t.Fatalf("DeriveHealth after start = %q, want stalled", got)
	}
}

func TestNewID(t *testing.T) {
	a, err := NewID("ses_")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewID("ses_")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "ses_") || len(a) != len("ses_")+24 {
		t.Fatalf("ids = %q, %q", a, b)
	}
}

// Package session defines the pure lifecycle for one bounded agent attempt.
// It owns no files, clocks, Git operations, or transport concerns.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Status is a durable session lifecycle state.
type Status string

const (
	StatusActive   Status = "active"
	StatusFinished Status = "finished"
	StatusCanceled Status = "canceled"
)

// Health is the derived supervision state of a session.
type Health string

const (
	HealthActive   Health = "active"
	HealthStalled  Health = "stalled"
	HealthFinished Health = "finished"
	HealthCanceled Health = "canceled"
)

var (
	// ErrTerminal is returned when a terminal session is mutated.
	ErrTerminal = errors.New("session is terminal")
	// ErrSummaryRequired is returned when finish has no meaningful summary.
	ErrSummaryRequired = errors.New("session summary is required")
	// ErrReasonRequired is returned when cancellation has no reason.
	ErrReasonRequired = errors.New("session cancellation reason is required")
)

// Session is the durable record of one actor working on one task.
type Session struct {
	ID             string     `yaml:"id" json:"id"`
	TaskID         string     `yaml:"task" json:"task"`
	AttemptID      string     `yaml:"attempt" json:"attempt"`
	Actor          string     `yaml:"actor" json:"actor"`
	Client         string     `yaml:"client,omitempty" json:"client,omitempty"`
	Model          string     `yaml:"model,omitempty" json:"model,omitempty"`
	Status         Status     `yaml:"status" json:"status"`
	IdempotencyKey string     `yaml:"idempotency_key" json:"idempotencyKey"`
	StartedAt      time.Time  `yaml:"started_at" json:"startedAt"`
	EndedAt        *time.Time `yaml:"ended_at,omitempty" json:"endedAt,omitempty"`
	Branch         string     `yaml:"branch,omitempty" json:"branch,omitempty"`
	HeadStarted    string     `yaml:"head_started,omitempty" json:"headStarted,omitempty"`
	HeadFinished   string     `yaml:"head_finished,omitempty" json:"headFinished,omitempty"`
	Summary        string     `yaml:"summary,omitempty" json:"summary,omitempty"`
	CancelReason   string     `yaml:"cancel_reason,omitempty" json:"cancelReason,omitempty"`
}

// Live is ephemeral supervision state. It is local and never committed to Git.
type Live struct {
	SessionID   string    `json:"session"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
	Progress    string    `json:"progress,omitempty"`
	Worktree    string    `json:"worktree,omitempty"`
}

// Finish returns a terminal copy of s.
func Finish(s Session, summary, head string, at time.Time) (Session, error) {
	if s.Status != StatusActive {
		return Session{}, fmt.Errorf("%w: cannot finish %q", ErrTerminal, s.Status)
	}
	if summary == "" {
		return Session{}, ErrSummaryRequired
	}
	s.Status = StatusFinished
	s.Summary = summary
	s.HeadFinished = head
	endedAt := at.UTC()
	s.EndedAt = &endedAt
	return s, nil
}

// Cancel returns a canceled copy of s.
func Cancel(s Session, reason string, at time.Time) (Session, error) {
	if s.Status != StatusActive {
		return Session{}, fmt.Errorf("%w: cannot cancel %q", ErrTerminal, s.Status)
	}
	if reason == "" {
		return Session{}, ErrReasonRequired
	}
	s.Status = StatusCanceled
	s.CancelReason = reason
	endedAt := at.UTC()
	s.EndedAt = &endedAt
	return s, nil
}

// DeriveHealth computes health from durable state and the latest heartbeat.
func DeriveHealth(s Session, live *Live, now time.Time, staleAfter time.Duration) Health {
	switch s.Status {
	case StatusFinished:
		return HealthFinished
	case StatusCanceled:
		return HealthCanceled
	}
	lastSeen := s.StartedAt
	if live != nil && live.HeartbeatAt.After(lastSeen) {
		lastSeen = live.HeartbeatAt
	}
	if now.Sub(lastSeen) > staleAfter {
		return HealthStalled
	}
	return HealthActive
}

// NewID returns a collision-resistant session or attempt id with the supplied prefix.
func NewID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

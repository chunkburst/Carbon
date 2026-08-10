package backup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// RetryPolicy controls retryable object-store operations. The zero value uses
// a short bounded exponential retry policy.
type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.InitialDelay <= 0 {
		p.InitialDelay = 100 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = time.Second
	}
	if p.MaxDelay < p.InitialDelay {
		p.MaxDelay = p.InitialDelay
	}
	return p
}

// Retry runs operation until it succeeds, is not retryable, its bounded retry
// budget is exhausted, or ctx is cancelled. It never retries checksum,
// immutability, cancellation, or not-found failures.
func Retry(ctx context.Context, policy RetryPolicy, operation func(context.Context) error) error {
	if operation == nil {
		return errors.New("backup retry operation is nil")
	}
	policy = policy.normalized()
	var last error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation(ctx)
		if err == nil {
			return nil
		}
		last = err
		if attempt == policy.MaxAttempts || !isRetryable(err) {
			break
		}
		delay := policy.InitialDelay
		for step := 1; step < attempt && delay < policy.MaxDelay; step++ {
			delay *= 2
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("backup operation failed after %d attempts: %w", policy.MaxAttempts, last)
}

type retryable interface{ Retryable() bool }
type temporary interface{ Temporary() bool }
type httpStatusCoder interface{ HTTPStatusCode() int }

func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrChecksumMismatch) ||
		errors.Is(err, ErrImmutableConflict) {
		return false
	}
	var marked retryable
	if errors.As(err, &marked) {
		return marked.Retryable()
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return networkErr.Timeout() || networkErr.Temporary()
	}
	var temp temporary
	if errors.As(err, &temp) && temp.Temporary() {
		return true
	}
	var status httpStatusCoder
	if errors.As(err, &status) {
		code := status.HTTPStatusCode()
		return code == 408 || code == 429 || code >= 500
	}
	return false
}

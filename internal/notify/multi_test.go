package notify

import (
	"context"
	"errors"
	"testing"
)

type stubChannel struct {
	name  string
	calls int
	err   error
}

func (s *stubChannel) Name() string { return s.name }
func (s *stubChannel) Send(_ context.Context, _ []FiledItem, _ int) error {
	s.calls++
	return s.err
}

func TestMultiNotifierFanOutAndResilience(t *testing.T) {
	failing := &stubChannel{name: "broken", err: errors.New("connection refused")}
	healthy := &stubChannel{name: "healthy"}
	errorOnly := NewFilteredNotifier(&stubChannel{name: "err-only"}, EventFilter{OnSuccess: false, OnError: true})

	m := NewMultiNotifier(nil, failing, healthy, errorOnly)
	err := m.Send(context.Background(), []FiledItem{{Filename: "a.pdf", Folder: "Finance/2026"}}, 0)
	if err == nil {
		t.Fatalf("expected combined error when one channel fails")
	}
	if failing.calls != 1 {
		t.Errorf("expected failing channel to be called once, got %d", failing.calls)
	}
	if healthy.calls != 1 {
		t.Errorf("expected healthy channel to still be called despite earlier failure, got %d", healthy.calls)
	}
	if errorOnly.Inner.(*stubChannel).calls != 0 {
		t.Errorf("expected error-only channel to be skipped on pure success, got %d", errorOnly.Inner.(*stubChannel).calls)
	}
}

func TestSMTPNotifierStub(t *testing.T) {
	s := NewSMTPNotifier(SMTPConfig{Host: "smtp.example.com", Port: 587}, nil)
	if s.Name() != "smtp" {
		t.Fatalf("expected Name() == 'smtp', got %q", s.Name())
	}
	if err := s.Send(context.Background(), []FiledItem{{Filename: "a.pdf", Folder: "Home/2026"}}, 0); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

package main

import (
	"context"
	"testing"
	"time"
)

func TestStateStoreOffsetRoundTrip(t *testing.T) {
	store, err := NewStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateStore returned error: %v", err)
	}

	if got, err := store.LoadOffset(context.Background()); err != nil || got != 0 {
		t.Fatalf("initial offset = %d, err=%v; want 0, nil", got, err)
	}
	if err := store.SaveOffset(context.Background(), 43); err != nil {
		t.Fatalf("SaveOffset returned error: %v", err)
	}
	if got, err := store.LoadOffset(context.Background()); err != nil || got != 43 {
		t.Fatalf("offset = %d, err=%v; want 43, nil", got, err)
	}
}

func TestStateStoreJobHistoryRoundTrip(t *testing.T) {
	store, err := NewStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateStore returned error: %v", err)
	}

	now := time.Now().UTC()
	jobs := []JobSnapshot{{
		ID:          "j1",
		ChatID:      123,
		ReplyTo:     7,
		Message:     "hello",
		State:       JobSucceeded,
		AgentName:   "engineer",
		CreatedAt:   now,
		StartedAt:   now,
		FinishedAt:  now,
		ForceDebate: true,
	}}
	if err := store.SaveJobHistory(jobs); err != nil {
		t.Fatalf("SaveJobHistory returned error: %v", err)
	}

	got, err := store.LoadJobHistory(context.Background())
	if err != nil {
		t.Fatalf("LoadJobHistory returned error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "j1" || got[0].AgentName != "engineer" || !got[0].ForceDebate {
		t.Fatalf("unexpected job history: %#v", got)
	}
}

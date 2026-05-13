package store

import (
	"context"
	"errors"
	"testing"
)

func TestSettings_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Miss before any write.
	if _, err := s.GetSetting(ctx, "missing"); !errors.Is(err, ErrSettingNotFound) {
		t.Fatalf("want ErrSettingNotFound, got %v", err)
	}

	// Write and read back.
	if err := s.PutSetting(ctx, "k", "v1"); err != nil {
		t.Fatalf("PutSetting: %v", err)
	}
	got, err := s.GetSetting(ctx, "k")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if got != "v1" {
		t.Fatalf("got %q want %q", got, "v1")
	}

	// Upsert overwrites in place.
	if err := s.PutSetting(ctx, "k", "v2"); err != nil {
		t.Fatalf("PutSetting overwrite: %v", err)
	}
	got, err = s.GetSetting(ctx, "k")
	if err != nil {
		t.Fatalf("GetSetting after upsert: %v", err)
	}
	if got != "v2" {
		t.Fatalf("got %q want %q after upsert", got, "v2")
	}

	// Empty values are allowed (distinguished from "missing").
	if err := s.PutSetting(ctx, "k", ""); err != nil {
		t.Fatalf("PutSetting empty: %v", err)
	}
	got, err = s.GetSetting(ctx, "k")
	if err != nil {
		t.Fatalf("GetSetting empty: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}

	// Reject empty key on write.
	if err := s.PutSetting(ctx, "", "x"); err == nil {
		t.Fatalf("expected error on empty key")
	}
}

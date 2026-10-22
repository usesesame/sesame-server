package syncsvc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type recordingTx struct {
	Tx
	recorded *[]string
}

func (t recordingTx) RecordNonce(scope, nonce string, now, expiresAt time.Time) (bool, error) {
	*t.recorded = append(*t.recorded, scope+"/"+nonce)
	return true, nil
}

type scriptedStorage struct {
	Storage
	updates         int
	recorded        []string
	failRecovery    error
	recoveryContext context.Context
}

func (s *scriptedStorage) Update(ctx context.Context, fn func(Tx) error) error {
	s.updates++
	if s.updates == 1 {
		scratch := []string{}
		_ = fn(recordingTx{recorded: &scratch})
		return errors.New("the request failed after its signature verified")
	}
	s.recoveryContext = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failRecovery != nil {
		return s.failRecovery
	}
	return fn(recordingTx{recorded: &s.recorded})
}

func spend(t *testing.T, storage Storage, ctx context.Context) error {
	t.Helper()
	keeper := nonceKeeper{inner: storage}
	return keeper.Update(ctx, func(tx Tx) error {
		if _, err := tx.RecordNonce("device/one", "nonce-one", time.Unix(100, 0), time.Unix(700, 0)); err != nil {
			return err
		}
		return errors.New("the request failed after its signature verified")
	})
}

func TestARequestThatFailedAfterVerificationStaysSpentEvenWhenItsContextIsCancelled(t *testing.T) {
	storage := &scriptedStorage{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := spend(t, storage, ctx)

	if err == nil || !strings.Contains(err.Error(), "failed after its signature verified") {
		t.Fatalf("the original failure was not returned: %v", err)
	}
	if len(storage.recorded) != 1 || storage.recorded[0] != "device/one/nonce-one" {
		t.Fatalf("the nonce was not kept spent: %v", storage.recorded)
	}
	if _, hasDeadline := storage.recoveryContext.Deadline(); !hasDeadline {
		t.Fatal("the recovery write has no deadline of its own")
	}
}

func TestAFailedRecoveryWriteIsLoggedAndTheOriginalFailureStillReturned(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	storage := &scriptedStorage{failRecovery: errors.New("fictional database outage")}

	err := spend(t, storage, context.Background())

	if err == nil || !strings.Contains(err.Error(), "failed after its signature verified") {
		t.Fatalf("the original failure was not returned: %v", err)
	}
	if !strings.Contains(logged.String(), "fictional database outage") {
		t.Fatalf("the failed recovery was not logged: %q", logged.String())
	}
	if strings.Contains(logged.String(), "nonce-one") {
		t.Fatalf("the log names the nonce: %q", logged.String())
	}
}

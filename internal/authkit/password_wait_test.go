package authkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func occupyArgonSlots(t *testing.T, wait time.Duration) func() {
	t.Helper()
	original := argonSlotWait
	argonSlotWait = wait
	for range cap(argonSlots) {
		argonSlots <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range cap(argonSlots) {
				<-argonSlots
			}
		})
	}
	t.Cleanup(func() {
		release()
		argonSlotWait = original
	})
	return release
}

const fictionalEncodedHash = "$argon2id$v=19$m=19456,t=2,p=1$ZmljdGlvbmFsLXNhbHQtMQ$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestPasswordWorkGivesUpWhenEverySlotStaysBusy(t *testing.T) {
	probe := installDerivationProbe(t, 8)
	close(probe.release)
	occupyArgonSlots(t, 20*time.Millisecond)
	ctx := context.Background()

	if ok, err := VerifyPasswordContext(ctx, fictionalEncodedHash, "fictional password"); ok || !errors.Is(err, ErrPasswordWorkBusy) {
		t.Fatalf("verify = %v, %v, want a busy error and no verdict", ok, err)
	}
	if ok, err := VerifyPasswordContext(ctx, "not-a-hash", "fictional password"); ok || !errors.Is(err, ErrPasswordWorkBusy) {
		t.Fatalf("verify of a broken hash = %v, %v, want a busy error", ok, err)
	}
	if encoded, err := HashPasswordContext(ctx, "fictional password"); encoded != "" || !errors.Is(err, ErrPasswordWorkBusy) {
		t.Fatalf("hash = %q, %v, want a busy error", encoded, err)
	}
	if err := DummyVerifyPasswordContext(ctx); !errors.Is(err, ErrPasswordWorkBusy) {
		t.Fatalf("dummy verify = %v, want a busy error", err)
	}
	if calls := probe.calls.Load(); calls != 0 {
		t.Fatalf("derivations = %d while every slot was busy, want 0", calls)
	}
}

func TestPasswordWorkStopsWaitingWhenTheRequestEnds(t *testing.T) {
	occupyArgonSlots(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := VerifyPasswordContext(ctx, fictionalEncodedHash, "fictional password")
	if !errors.Is(err, ErrPasswordWorkBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("verify after cancellation = %v, want busy and canceled", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("waited %s after the request ended", waited)
	}
}

func TestPasswordWorkTakesASlotThatFreesWithinTheWait(t *testing.T) {
	probe := installDerivationProbe(t, 8)
	close(probe.release)
	release := occupyArgonSlots(t, 5*time.Second)
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	if _, err := VerifyPasswordContext(context.Background(), fictionalEncodedHash, "fictional password"); err != nil {
		t.Fatalf("verify after a slot freed = %v", err)
	}
	if calls := probe.calls.Load(); calls != 1 {
		t.Fatalf("derivations = %d, want 1", calls)
	}
}

func TestPasswordWorkWithAContextGivesTheSameVerdicts(t *testing.T) {
	ctx := context.Background()
	encoded, err := HashPasswordContext(ctx, "fictional correct password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(encoded, "fictional correct password") {
		t.Fatal("the plain verifier rejected a hash made with a context")
	}
	if ok, err := VerifyPasswordContext(ctx, encoded, "fictional correct password"); !ok || err != nil {
		t.Fatalf("verify correct = %v, %v", ok, err)
	}
	if ok, err := VerifyPasswordContext(ctx, encoded, "fictional wrong password"); ok || err != nil {
		t.Fatalf("verify wrong = %v, %v, want false without an error", ok, err)
	}
	if ok, err := VerifyPasswordContext(ctx, "not-a-hash", "fictional correct password"); ok || err != nil {
		t.Fatalf("verify broken hash = %v, %v, want false without an error", ok, err)
	}
}

func TestContextPasswordWorkStaysWithinTheLimit(t *testing.T) {
	const callers = 18
	probe := installDerivationProbe(t, callers)
	original := argonSlotWait
	argonSlotWait = time.Minute
	t.Cleanup(func() { argonSlotWait = original })
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			switch index % 3 {
			case 0:
				_, _ = HashPasswordContext(context.Background(), "fictional password for a burst")
			case 1:
				_, _ = VerifyPasswordContext(context.Background(), fictionalEncodedHash, "fictional password for a burst")
			default:
				_ = DummyVerifyPasswordContext(context.Background())
			}
		}()
	}
	limit := cap(argonSlots)
	for range limit {
		<-probe.entered
	}
	close(probe.release)
	group.Wait()
	if peak := probe.peak.Load(); peak > int64(limit) {
		t.Fatalf("peak derivations in flight = %d, limit %d", peak, limit)
	}
	if calls := probe.calls.Load(); calls != callers {
		t.Fatalf("derivations = %d, want %d", calls, callers)
	}
}

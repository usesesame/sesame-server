package syncsvc

import (
	"context"
	"log/slog"
	"time"
)

const nonceRecoveryTimeout = 5 * time.Second

type spentNonce struct {
	scope   string
	nonce   string
	now     time.Time
	expires time.Time
}

type nonceTx struct {
	Tx
	spent *spentNonce
}

func (t *nonceTx) RecordNonce(scope, nonce string, now, expiresAt time.Time) (bool, error) {
	fresh, err := t.Tx.RecordNonce(scope, nonce, now, expiresAt)
	if err == nil && fresh {
		t.spent = &spentNonce{scope: scope, nonce: nonce, now: now, expires: expiresAt}
	}
	return fresh, err
}

type nonceKeeper struct {
	inner Storage
}

func (k nonceKeeper) View(ctx context.Context, fn func(Tx) error) error {
	return k.inner.View(ctx, fn)
}

func (k nonceKeeper) Update(ctx context.Context, fn func(Tx) error) error {
	var tracked *nonceTx
	err := k.inner.Update(ctx, func(inner Tx) error {
		tracked = &nonceTx{Tx: inner}
		return fn(tracked)
	})
	if err == nil || tracked == nil || tracked.spent == nil {
		return err
	}
	spent := tracked.spent
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), nonceRecoveryTimeout)
	defer cancel()
	if recordErr := k.inner.Update(recovery, func(inner Tx) error {
		_, recordErr := inner.RecordNonce(spent.scope, spent.nonce, spent.now, spent.expires)
		return recordErr
	}); recordErr != nil {
		slog.Error("could not keep the nonce of a failed signed request spent", "error", recordErr)
	}
	return err
}

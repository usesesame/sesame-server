package authkit

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

type derivationProbe struct {
	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
	entered  chan struct{}
	release  chan struct{}
}

func installDerivationProbe(t *testing.T, buffer int) *derivationProbe {
	t.Helper()
	probe := &derivationProbe{entered: make(chan struct{}, buffer), release: make(chan struct{})}
	original := deriveKey
	deriveKey = func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte {
		current := probe.inFlight.Add(1)
		defer probe.inFlight.Add(-1)
		for {
			peak := probe.peak.Load()
			if current <= peak || probe.peak.CompareAndSwap(peak, current) {
				break
			}
		}
		probe.calls.Add(1)
		probe.entered <- struct{}{}
		<-probe.release
		return make([]byte, keyLength)
	}
	t.Cleanup(func() { deriveKey = original })
	return probe
}

func TestArgonConcurrencyLimitStaysBetweenTwoAndFour(t *testing.T) {
	for cpus, want := range map[int]int{0: 2, 1: 2, 2: 2, 3: 3, 4: 4, 16: 4, 128: 4} {
		if got := argonConcurrencyLimit(cpus); got != want {
			t.Fatalf("limit for %d CPUs = %d, want %d", cpus, got, want)
		}
	}
	if capacity := cap(argonSlots); capacity < 2 || capacity > 4 {
		t.Fatalf("process limit = %d", capacity)
	}
}

func TestConcurrentPasswordWorkNeverExceedsTheLimit(t *testing.T) {
	const callers = 24
	probe := installDerivationProbe(t, callers)
	encoded := "$argon2id$v=19$m=19456,t=2,p=1$ZmljdGlvbmFsLXNhbHQtMQ$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			switch index % 3 {
			case 0:
				_, _ = HashPassword("fictional password for a burst")
			case 1:
				_ = VerifyPassword(encoded, "fictional password for a burst")
			default:
				DummyVerifyPassword()
			}
		}()
	}
	limit := cap(argonSlots)
	for range limit {
		<-probe.entered
	}
	for range 10000 {
		runtime.Gosched()
	}
	close(probe.release)
	group.Wait()
	if peak := probe.peak.Load(); peak != int64(limit) {
		t.Fatalf("peak derivations in flight = %d, limit %d", peak, limit)
	}
	if calls := probe.calls.Load(); calls != callers {
		t.Fatalf("derivations = %d, want %d", calls, callers)
	}
}

func TestVerifyPasswordSpendsOneDerivationOnAMissingOrBrokenHash(t *testing.T) {
	probe := installDerivationProbe(t, 16)
	close(probe.release)
	for _, encoded := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=0,t=2,p=1$ZmljdGlvbmFsLXNhbHQtMQ$AAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$short$AAAA",
	} {
		before := probe.calls.Load()
		if VerifyPassword(encoded, "fictional password") {
			t.Fatalf("%q verified", encoded)
		}
		if spent := probe.calls.Load() - before; spent != 1 {
			t.Fatalf("%q spent %d derivations, want 1", encoded, spent)
		}
	}
}

func TestVerifyPasswordRefusesParallelismBeyondOneByte(t *testing.T) {
	encoded := "$argon2id$v=19$m=19456,t=2,p=256$ZmljdGlvbmFsLXNhbHQtMQ$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, _, _, _, _, ok := parseArgon2id(encoded); ok {
		t.Fatal("a parallelism of 256 was accepted")
	}
	if VerifyPassword(encoded, "fictional password") {
		t.Fatal("a hash with a parallelism of 256 verified")
	}
}

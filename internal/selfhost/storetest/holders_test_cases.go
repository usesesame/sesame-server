package storetest

import (
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

func testMembers(t *testing.T, h Harness) {
	each(t, h, "members are named uniquely without regard to case", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		_, err := f.store.CreateMember(f.ctx, by, "sam")
		f.expect(err, selfhost.ErrConflict)
		for _, name := range []string{"", "   ", strings.Repeat("x", 65), "bad\nname", "bad\x00name"} {
			_, err = f.store.CreateMember(f.ctx, by, name)
			f.expect(err, selfhost.ErrInvalidInput)
		}
		other := f.member(by, "Robin")
		_, err = f.store.RenameMember(f.ctx, by, other.ID, "SAM")
		f.expect(err, selfhost.ErrConflict)
		renamed, err := f.store.RenameMember(f.ctx, by, other.ID, "Robin Lee")
		f.must(err)
		if renamed.Name != "Robin Lee" {
			t.Fatalf("unexpected name %q", renamed.Name)
		}
		_, err = f.store.RenameMember(f.ctx, by, "missing", "Whoever")
		f.expect(err, selfhost.ErrNotFound)
		members, err := f.store.ListMembers(f.ctx)
		f.must(err)
		if len(members) != 2 || findMember(members, member.ID) == nil || findMember(members, other.ID).Name != "Robin Lee" {
			t.Fatalf("unexpected members %+v", members)
		}
	})
	each(t, h, "deleting a member revokes devices and cancels pairings", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		keep := f.member(by, "Robin")
		first := f.pair(by, memberHolder(member), "Sam laptop")
		second := f.pair(by, memberHolder(member), "Sam desktop")
		kept := f.pair(by, memberHolder(keep), "Robin laptop")
		pending, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		members, err := f.store.ListMembers(f.ctx)
		f.must(err)
		if counted := findMember(members, member.ID); counted == nil || counted.DeviceCount != 2 {
			t.Fatalf("unexpected members %+v", members)
		}
		revoked, err := f.store.DeleteMember(f.ctx, by, member.ID)
		f.must(err)
		if revoked != 2 {
			t.Fatalf("revoked %d devices", revoked)
		}
		for _, token := range []string{first.Token, second.Token} {
			_, err = f.store.AuthenticateDevice(f.ctx, token)
			f.expect(err, selfhost.ErrDeviceInvalid)
			_, err = f.store.Heartbeat(f.ctx, token, selfhost.DeviceMeta{ProtocolVersion: 1})
			f.expect(err, selfhost.ErrDeviceInvalid)
		}
		_, err = f.store.AuthenticateDevice(f.ctx, kept.Token)
		f.must(err)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: pending.Code, DeviceName: "late"})
		f.expect(err, selfhost.ErrPairingInvalid)
		_, err = f.store.DeleteMember(f.ctx, by, member.ID)
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.RevokeMemberDevices(f.ctx, by, member.ID)
		f.expect(err, selfhost.ErrNotFound)
		f.member(by, "Sam")
		all, err := f.store.ListDevices(f.ctx, selfhost.DeviceFilter{IncludeInactive: true})
		f.must(err)
		if len(all) != 3 {
			t.Fatalf("%d devices kept for the record", len(all))
		}
	})
}

func findMember(members []selfhost.Member, id string) *selfhost.Member {
	for index := range members {
		if members[index].ID == id {
			return &members[index]
		}
	}
	return nil
}

func testPairing(t *testing.T, h Harness) {
	each(t, h, "a code redeems once and the token works", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member), DeviceNameHint: "Sam laptop"})
		f.must(err)
		if !selfhost.ValidPairingCode(issued.Code) {
			t.Fatalf("code length %d", len(issued.Code))
		}
		if !issued.Pairing.ExpiresAt.Equal(f.clock.Now().Add(selfhost.PairingTTL)) || issued.Pairing.Holder.Name != "Sam" {
			t.Fatalf("unexpected pairing %+v", issued.Pairing)
		}
		device, err := f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "  Sam laptop ", Meta: selfhost.DeviceMeta{AppVersion: "0.3.1", Platform: "linux", Architecture: "x86_64", UpdateChannel: "stable"}})
		f.must(err)
		if device.Token == "" || device.Device.Name != "Sam laptop" || device.Device.Holder.ID != member.ID || device.Device.Holder.Kind != selfhost.HolderMember {
			t.Fatalf("unexpected device %+v", device)
		}
		if device.Device.Meta.ProtocolVersion != 1 || !device.Device.ExpiresAt.Equal(f.clock.Now().Add(selfhost.DeviceTokenTTL)) {
			t.Fatalf("unexpected device meta %+v", device.Device)
		}
		known, err := f.store.AuthenticateDevice(f.ctx, device.Token)
		f.must(err)
		if known.ID != device.Device.ID {
			t.Fatal("authenticated a different device")
		}
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Replay"})
		f.expect(err, selfhost.ErrPairingInvalid)
		pending, err := f.store.ListPairings(f.ctx)
		f.must(err)
		if len(pending) != 0 {
			t.Fatalf("used pairing is still listed: %+v", pending)
		}
	})
	each(t, h, "owners can pair their own devices", func(f *fixture) {
		owner := f.firstOwner("Avery")
		device := f.pair(actorOf(owner), ownerHolder(owner), "Avery desktop")
		if device.Device.Holder.Kind != selfhost.HolderOwner || device.Device.Holder.Name != "Avery" {
			t.Fatalf("unexpected holder %+v", device.Device.Holder)
		}
		_, err := f.store.CreatePairing(f.ctx, actorOf(owner), selfhost.PairingInput{Holder: selfhost.Holder{Kind: selfhost.HolderOwner, ID: "missing"}})
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.CreatePairing(f.ctx, actorOf(owner), selfhost.PairingInput{Holder: selfhost.Holder{Kind: "other", ID: "x"}})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.CreatePairing(f.ctx, actorOf(owner), selfhost.PairingInput{Holder: selfhost.Holder{Kind: selfhost.HolderOwner}})
		f.expect(err, selfhost.ErrInvalidInput)
	})
	each(t, h, "codes expire after ten minutes", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		f.clock.Advance(selfhost.PairingTTL - time.Second)
		pending, err := f.store.ListPairings(f.ctx)
		f.must(err)
		if len(pending) != 1 {
			t.Fatalf("%d pending pairings", len(pending))
		}
		f.clock.Advance(time.Second)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Late"})
		f.expect(err, selfhost.ErrPairingInvalid)
		pending, err = f.store.ListPairings(f.ctx)
		f.must(err)
		if len(pending) != 0 {
			t.Fatal("expired pairing is still listed")
		}
		err = f.store.CancelPairing(f.ctx, by, issued.Pairing.ID)
		f.expect(err, selfhost.ErrNotFound)
	})
	each(t, h, "cancel and replacement invalidate earlier codes", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		other := f.member(by, "Robin")
		first, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		unrelated, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(other)})
		f.must(err)
		second, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		if first.Code == second.Code {
			t.Fatal("replacement reused the code")
		}
		pending, err := f.store.ListPairings(f.ctx)
		f.must(err)
		if len(pending) != 2 {
			t.Fatalf("%d pending pairings", len(pending))
		}
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: first.Code, DeviceName: "Old"})
		f.expect(err, selfhost.ErrPairingInvalid)
		f.must(f.store.CancelPairing(f.ctx, by, second.Pairing.ID))
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: second.Code, DeviceName: "Cancelled"})
		f.expect(err, selfhost.ErrPairingInvalid)
		err = f.store.CancelPairing(f.ctx, by, second.Pairing.ID)
		f.expect(err, selfhost.ErrNotFound)
		err = f.store.CancelPairing(f.ctx, by, "missing")
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: unrelated.Code, DeviceName: "Fine"})
		f.must(err)
	})
	each(t, h, "malformed codes and names are refused without using a code", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		for _, code := range []string{"", "short", strings.Repeat("a", 31), strings.Repeat("a", 129), strings.Repeat("a", 43)} {
			_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: code, DeviceName: "Name"})
			f.expect(err, selfhost.ErrPairingInvalid)
		}
		for _, name := range []string{"", "  ", strings.Repeat("n", 65), "tab\tname", "café"} {
			_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: name})
			f.expect(err, selfhost.ErrInvalidInput)
		}
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Name", Meta: selfhost.DeviceMeta{AppVersion: strings.Repeat("v", 65)}})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Name", Meta: selfhost.DeviceMeta{ProtocolVersion: 101}})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member), DeviceNameHint: "bad\x01"})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Name"})
		f.must(err)
	})
	each(t, h, "codes and tokens are stored only as hashes", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		var stored []byte
		f.must(f.raw().QueryRow(`SELECT code_hash FROM pairings`).Scan(&stored))
		want := sha256.Sum256([]byte(issued.Code))
		if string(stored) != string(want[:]) {
			t.Fatal("pairing code hash is not the sha256 of the code")
		}
		device, err := f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Name"})
		f.must(err)
		f.must(f.raw().QueryRow(`SELECT token_hash FROM devices`).Scan(&stored))
		want = sha256.Sum256([]byte(device.Token))
		if string(stored) != string(want[:]) {
			t.Fatal("device token hash is not the sha256 of the token")
		}
	})
	each(t, h, "concurrent redemptions have one winner", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: memberHolder(member)})
		f.must(err)
		const attempts = 24
		var wg sync.WaitGroup
		type result struct {
			device selfhost.IssuedDevice
			err    error
		}
		results := make(chan result, attempts)
		start := make(chan struct{})
		for index := 0; index < attempts; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				device, err := f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: "Racer"})
				results <- result{device, err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		var winners []selfhost.IssuedDevice
		for r := range results {
			switch {
			case r.err == nil:
				winners = append(winners, r.device)
			case !errors.Is(r.err, selfhost.ErrPairingInvalid):
				t.Fatalf("unexpected error %v", r.err)
			}
		}
		if len(winners) != 1 {
			t.Fatalf("%d redemptions succeeded", len(winners))
		}
		devices, err := f.store.ListDevices(f.ctx, selfhost.DeviceFilter{IncludeInactive: true})
		f.must(err)
		if len(devices) != 1 || devices[0].ID != winners[0].Device.ID {
			t.Fatalf("unexpected devices %+v", devices)
		}
	})
}

func testDevices(t *testing.T, h Harness) {
	each(t, h, "heartbeat updates metadata and keeps the expiry", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		device := f.pair(by, memberHolder(member), "Sam laptop")
		f.clock.Advance(time.Hour)
		updated, err := f.store.Heartbeat(f.ctx, device.Token, selfhost.DeviceMeta{
			AppVersion: "0.4.0", Platform: "linux", Architecture: "arm64", UpdateChannel: "stable", ProtocolVersion: 2,
			BrowserHelperCapable: true, BrowserHelperObserved: true,
		})
		f.must(err)
		if updated.Meta.AppVersion != "0.4.0" || updated.Meta.Architecture != "arm64" || updated.Meta.ProtocolVersion != 2 || !updated.Meta.BrowserHelperCapable {
			t.Fatalf("unexpected device %+v", updated)
		}
		if !updated.LastSeenAt.Equal(f.clock.Now()) || updated.BrowserHelperLastObservedAt == nil || !updated.ExpiresAt.Equal(device.Device.ExpiresAt) {
			t.Fatalf("unexpected times %+v", updated)
		}
		later, err := f.store.Heartbeat(f.ctx, device.Token, selfhost.DeviceMeta{ProtocolVersion: 2})
		f.must(err)
		if later.BrowserHelperLastObservedAt == nil || !later.BrowserHelperLastObservedAt.Equal(*updated.BrowserHelperLastObservedAt) {
			t.Fatal("an unobserved heartbeat must keep the last observation")
		}
		for _, meta := range []selfhost.DeviceMeta{{}, {ProtocolVersion: 101}, {ProtocolVersion: 1, Platform: strings.Repeat("p", 65)}, {ProtocolVersion: 1, AppVersion: "a\nb"}} {
			_, err = f.store.Heartbeat(f.ctx, device.Token, meta)
			f.expect(err, selfhost.ErrInvalidInput)
		}
		_, err = f.store.Heartbeat(f.ctx, "unknown", selfhost.DeviceMeta{ProtocolVersion: 1})
		f.expect(err, selfhost.ErrDeviceInvalid)
	})
	each(t, h, "tokens expire after ninety days", func(f *fixture) {
		owner := f.firstOwner("Avery")
		device := f.pair(actorOf(owner), ownerHolder(owner), "Laptop")
		f.clock.Advance(selfhost.DeviceTokenTTL - time.Second)
		_, err := f.store.Heartbeat(f.ctx, device.Token, selfhost.DeviceMeta{ProtocolVersion: 1})
		f.must(err)
		f.clock.Advance(time.Second)
		_, err = f.store.AuthenticateDevice(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		_, err = f.store.Heartbeat(f.ctx, device.Token, selfhost.DeviceMeta{ProtocolVersion: 1})
		f.expect(err, selfhost.ErrDeviceInvalid)
		err = f.store.Disconnect(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		active, err := f.store.ListDevices(f.ctx, selfhost.DeviceFilter{})
		f.must(err)
		if len(active) != 0 {
			t.Fatal("expired device is listed as active")
		}
	})
	each(t, h, "revoked and stale tokens stop working", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		old := f.pair(by, memberHolder(member), "Sam laptop")
		f.must(f.store.RevokeDevice(f.ctx, by, old.Device.ID))
		f.must(f.store.RevokeDevice(f.ctx, by, old.Device.ID))
		err := f.store.RevokeDevice(f.ctx, by, "missing")
		f.expect(err, selfhost.ErrNotFound)
		fresh := f.pair(by, memberHolder(member), "Sam laptop")
		_, err = f.store.AuthenticateDevice(f.ctx, old.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		_, err = f.store.Heartbeat(f.ctx, old.Token, selfhost.DeviceMeta{ProtocolVersion: 1})
		f.expect(err, selfhost.ErrDeviceInvalid)
		_, err = f.store.AuthenticateDevice(f.ctx, fresh.Token)
		f.must(err)
		for _, token := range []string{"", "x", old.Token + "x", strings.Repeat("a", 300)} {
			_, err = f.store.AuthenticateDevice(f.ctx, token)
			f.expect(err, selfhost.ErrDeviceInvalid)
		}
		active, err := f.store.ListDevices(f.ctx, selfhost.DeviceFilter{})
		f.must(err)
		all, err := f.store.ListDevices(f.ctx, selfhost.DeviceFilter{IncludeInactive: true})
		f.must(err)
		if len(active) != 1 || len(all) != 2 {
			t.Fatalf("active %d all %d", len(active), len(all))
		}
		for _, device := range all {
			if (device.ID == old.Device.ID) != (device.RevokedAt != nil) {
				t.Fatalf("unexpected revoked state %+v", device)
			}
		}
	})
	each(t, h, "a device can disconnect itself", func(f *fixture) {
		owner := f.firstOwner("Avery")
		device := f.pair(actorOf(owner), ownerHolder(owner), "Laptop")
		f.must(f.store.Disconnect(f.ctx, device.Token))
		_, err := f.store.AuthenticateDevice(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		err = f.store.Disconnect(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
	})
	each(t, h, "revoking by member leaves other members alone", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		other := f.member(by, "Robin")
		f.pair(by, memberHolder(member), "One")
		f.pair(by, memberHolder(member), "Two")
		kept := f.pair(by, memberHolder(other), "Three")
		count, err := f.store.RevokeMemberDevices(f.ctx, by, member.ID)
		f.must(err)
		if count != 2 {
			t.Fatalf("revoked %d", count)
		}
		count, err = f.store.RevokeMemberDevices(f.ctx, by, member.ID)
		f.must(err)
		if count != 0 {
			t.Fatalf("revoked %d on repeat", count)
		}
		_, err = f.store.AuthenticateDevice(f.ctx, kept.Token)
		f.must(err)
	})
}

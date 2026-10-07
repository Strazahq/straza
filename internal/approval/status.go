package approval

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// DeliveryRecord is the most recent delivery ATTEMPT on one channel, the
// console channel card's "is the pipe actually moving" signal. OK reflects the
// attempt's outcome; Note is a short operator-facing description (never
// approval content; the privacy envelope applies to telemetry too).
type DeliveryRecord struct {
	At   time.Time
	OK   bool
	Note string
}

// recordDelivery stores the last delivery attempt for a channel (called by the
// channel implementations after each real send, test sends included).
func (s *Service) recordDelivery(channel string, err error, note string) {
	rec := DeliveryRecord{At: s.now(), OK: err == nil, Note: note}
	if err != nil {
		rec.Note = note + ": " + err.Error()
	}
	s.dmu.Lock()
	if s.lastDelivery == nil {
		s.lastDelivery = map[string]DeliveryRecord{}
	}
	s.lastDelivery[channel] = rec
	s.dmu.Unlock()
}

// lastDeliveryFor returns the last recorded attempt for a channel, if any.
func (s *Service) lastDeliveryFor(channel string) (DeliveryRecord, bool) {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	rec, ok := s.lastDelivery[channel]
	return rec, ok
}

// ChannelStatus is one row of the admin channel-status surface.
type ChannelStatus struct {
	Name       string
	Configured bool
	Detail     string
	// Push only: enrolled approver devices vs registered push routes. The
	// gap between the two is THE diagnostic: a device without a push route
	// can decide but never rings.
	Devices       int
	Registrations int
	LastDelivery  *DeliveryRecord
}

// ChannelStatuses reports every notification channel the deployment knows:
// the always-on console plus each third-party channel, configured or not (an
// unconfigured row with its reason is the point: silence must be visible).
func (s *Service) ChannelStatuses(ctx context.Context) ([]ChannelStatus, error) {
	out := []ChannelStatus{{
		Name: policy.NotifyConsole, Configured: true,
		// One voice on the wire: plain words, the
		// same sentence the console renders, so strazactl and API readers
		// hear no architecture jargon either.
		Detail: "always available: approvers check it themselves, nothing is delivered",
	}}

	slack := ChannelStatus{Name: policy.NotifySlack, Configured: s.slack != nil}
	if s.slack != nil {
		slack.Detail = "posting to channel " + s.slack.channel
	} else {
		slack.Detail = "not configured (approval.channels.slack)"
	}
	out = append(out, slack)

	push := ChannelStatus{Name: policy.NotifyPush, Configured: s.push != nil}
	if s.push != nil {
		hosts := make([]string, 0, len(s.push.allowedHosts))
		for h := range s.push.allowedHosts {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		fcm := "fcm off"
		if s.push.fcm != nil {
			fcm = "fcm on"
		}
		wp := "webpush off"
		if s.push.webpush != nil {
			wp = "webpush on"
		}
		apns := "apns off"
		if s.push.apns != nil {
			apns = "apns on"
		}
		relay := "relay off"
		if s.push.relay != nil {
			relay = "relay on"
		}
		lanes := fcm + " · " + wp + " · " + apns + " · " + relay
		if len(hosts) == 0 {
			push.Detail = lanes + " · no push-host allowlist"
		} else {
			push.Detail = lanes + " · allowed hosts: " + joinComma(hosts)
		}
		devices, err := s.st.Approvers().CountDevices(ctx)
		if err != nil {
			return nil, err
		}
		targets, err := s.st.Approvers().ListPushTargets(ctx)
		if err != nil {
			return nil, err
		}
		push.Devices = devices
		push.Registrations = len(targets)
	} else {
		push.Detail = "not configured (approval.push: enable FCM, set allowedPushHosts, or set webpush.vapidKeyFile)"
	}
	out = append(out, push)

	for i := range out {
		if rec, ok := s.lastDeliveryFor(out[i].Name); ok {
			r := rec
			out[i].LastDelivery = &r
		}
	}
	return out, nil
}

func joinComma(ss []string) string {
	out := ""
	for i, v := range ss {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// TestTarget is one delivery attempt of a channel test.
type TestTarget struct {
	Target string
	OK     bool
	Error  string
}

// TestReport is the synchronous result of a channel test-send.
type TestReport struct {
	Channel string
	Targets []TestTarget
}

// Sentinel errors for the test surface (mapped to 400/404 by the API layer).
var (
	ErrChannelNotTestable = fmt.Errorf("approval: the console is a pull surface; there is nothing to send")
	ErrChannelUnknown     = fmt.Errorf("approval: unknown channel")
	ErrChannelOff         = fmt.Errorf("approval: channel is not configured")
)

// TestChannel fires a synchronous test notification through one channel and
// reports per-target outcomes. Test sends ride the SAME senders as real
// deliveries (allowlist, timeouts, pruning), so a green test is evidence the
// real pipe works; they also stamp lastDelivery like any attempt. The push
// test carries the standard opaque envelope with a synthetic reference; the
// mobile app treats it as a benign refresh hint, and a raw ntfy subscription
// shows it arriving.
func (s *Service) TestChannel(ctx context.Context, name string) (TestReport, error) {
	switch name {
	case policy.NotifyConsole:
		return TestReport{}, ErrChannelNotTestable
	case policy.NotifySlack:
		if s.slack == nil {
			return TestReport{}, ErrChannelOff
		}
		err := s.slack.testMessage(ctx)
		t := TestTarget{Target: "channel " + s.slack.channel, OK: err == nil}
		if err != nil {
			t.Error = err.Error()
		}
		return TestReport{Channel: name, Targets: []TestTarget{t}}, nil
	case policy.NotifyPush:
		if s.push == nil {
			return TestReport{}, ErrChannelOff
		}
		return s.push.test(ctx)
	default:
		return TestReport{}, ErrChannelUnknown
	}
}

// pushLaneMarker names the encryption lane a registration's send takes,
// " · keyed" (RFC 8291 aes128gcm) or " · legacy" (plain JSON), using the
// same predicate sendOne dispatches on, so the report can never disagree
// with the send path. Kinds without the keyed/legacy axis (fcm, apns) get
// no marker.
func pushLaneMarker(reg store.ApproverPushTarget) string {
	if reg.Kind != "unifiedpush" && reg.Kind != "webpush" {
		return ""
	}
	if _, _, _, keyed := splitKeyedEndpoint(reg.TokenOrEndpoint); keyed {
		return " · keyed"
	}
	return " · legacy"
}

// test fans a synchronous test push to every registration and gathers
// per-target outcomes (bounded by the per-send timeout each).
func (p *pushDelivery) test(ctx context.Context) (TestReport, error) {
	targets, err := p.st.Approvers().ListPushTargets(ctx)
	if err != nil {
		return TestReport{}, err
	}
	report := TestReport{Channel: policy.NotifyPush, Targets: make([]TestTarget, len(targets))}
	ref := "test:" + uuid.NewString()
	var wg sync.WaitGroup
	for i, reg := range targets {
		wg.Add(1)
		go func(i int, reg store.ApproverPushTarget) {
			defer wg.Done()
			t := TestTarget{Target: reg.Kind + " · device " + reg.DeviceID + pushLaneMarker(reg)}
			if err := p.sendOne(reg, pushKindStatus, ref, time.Time{}); err != nil {
				t.Error = err.Error()
			} else {
				t.OK = true
			}
			report.Targets[i] = t
		}(i, reg)
	}
	wg.Wait()
	return report, nil
}

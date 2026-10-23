package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

// Literal standalone witnesses exercise direct, interchange, walk and recovery
// presentation without benchmark data or a routing/oracle dependency.
func runtimeFixture() (model.Dataset, model.Query, []model.Journey) {
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "selector-runtime", TimeZone: "Etc/UTC", ServiceDate: "2026-10-20",
		Stops: []model.Stop{{ID: "a", Accessible: model.Yes}, {ID: "b", Accessible: model.Yes}, {ID: "c", Accessible: model.Yes}, {ID: "d", Accessible: model.Yes}},
		Trips: []model.Trip{{ID: "direct", RouteID: "r-direct", Accessible: model.Yes}, {ID: "first", RouteID: "r-first", Accessible: model.Yes}, {ID: "second", RouteID: "r-second", Accessible: model.Yes}, {ID: "later", RouteID: "r-second", Accessible: model.Yes}},
		Connections: []model.Connection{
			{ID: "direct", TripID: "direct", From: "a", To: "d", Departure: 10, Arrival: 100, Pickup: true, Dropoff: true},
			{ID: "first", TripID: "first", From: "a", To: "b", Departure: 0, Arrival: 40, Pickup: true, Dropoff: true},
			{ID: "second", TripID: "second", From: "c", To: "d", Departure: 60, Arrival: 90, Pickup: true, Dropoff: true},
			{ID: "later", TripID: "later", From: "c", To: "d", Departure: 180, Arrival: 210, Pickup: true, Dropoff: true}},
		Footpaths: []model.Footpath{{ID: "interchange", From: "b", To: "c", Duration: 10, Accessible: model.Yes}, {ID: "direct-walk", From: "a", To: "d", Duration: 150, Accessible: model.Yes}},
	}
	q := model.Query{ID: "q", Origin: "a", Destination: "d", Departure: 0, LatestArrival: 500, TransferBuffer: 5, MaxWalkSeconds: 200, MaxBoardings: 3, MaxLegs: 8}
	js := []model.Journey{
		{Legs: []model.Leg{{Kind: "ride", Ref: "direct", Departure: 10, Arrival: 100}}},
		{Legs: []model.Leg{{Kind: "ride", Ref: "first", Departure: 0, Arrival: 40}, {Kind: "walk", Ref: "interchange", Departure: 40, Arrival: 50}, {Kind: "ride", Ref: "second", Departure: 60, Arrival: 90}}},
		{Legs: []model.Leg{{Kind: "walk", Ref: "direct-walk", Departure: 0, Arrival: 150}}},
	}
	return d, q, js
}
func legacyRuntimeConfig() ChoiceConfig {
	return ChoiceConfig{MaxCandidates: 100, MaxSubsets: 10000, Groups: []PreferenceGroup{{Name: "explicit", Preferences: []Preference{{ID: "elapsed"}, {ID: "transfers", TransferSeconds: 300}, {ID: "walking", ExtraWalkWeight: 2}}}}}
}
func runtimeSafeConfig(objective string) ChoiceConfig {
	safe := finiteSafeTestConfig(objective)
	return ChoiceConfig{MaxCandidates: safe.MaxCandidates, MaxSubsets: safe.MaxStates, LeanDelivery: true, DeliveryPolicy: "finite20_safe_v1", DeliveryLimits: []int{3}, FiniteSafe: &safe}
}
func runtimeFillConfig(k int) ChoiceConfig {
	fill := fillTestConfig(k)
	return ChoiceConfig{MaxCandidates: fill.Base.MaxCandidates, MaxSubsets: fill.Base.MaxSubsets, LeanDelivery: true, DeliveryPolicy: "bounded_absolute_hybrid_fill_v1", DeliveryLimits: []int{k}, HybridFill: &fill}
}

func TestLegacyDefaultJSONUnchanged(t *testing.T) {
	d, q, js := runtimeFixture()
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	// Digests were obtained from HEAD's pre-integration choices.go/quality.go with
	// this literal fixture. Optional opt-in fields must not alter default JSON.
	for _, tc := range []struct {
		name    string
		choices bool
		want    string
	}{
		{name: "Evaluate", want: "87b39d1a01d35d597bb6c26f5dd906f12b2b5c034718b26f10a9bfbf9389b8ec"},
		{name: "EvaluateChoices", choices: true, want: "1013374593c3bf8973b3b75d0f05ba8d56b5157d810eaf7ccdb39fe5b82d0299"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r Result
			var err error
			if tc.choices {
				r, err = x.EvaluateChoices(context.Background(), q, js, legacyRuntimeConfig())
			} else {
				r, err = x.Evaluate(q, js)
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.Sum256(b)
			got := hex.EncodeToString(h[:])
			if got != tc.want {
				t.Fatalf("legacy JSON changed: got %s, want %s\n%s", got, tc.want, b)
			}
			if r.FeatureMode != "" || r.ContinuousChoices != nil || r.FiniteSafeChoices != nil || r.HybridFillChoices != nil || len(r.Selections) != 6 {
				t.Fatalf("opt-in state leaked into defaults: %+v", r)
			}
		})
	}
}

func TestLeanDeliveryMatchesRequestedLegacyOutputs(t *testing.T) {
	d, q, js := runtimeFixture()
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := x.EvaluateChoices(context.Background(), q, js, legacyRuntimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := legacyRuntimeConfig()
	cfg.LeanDelivery = true
	cfg.DeliveryPolicy = "finite_preferences_v2"
	cfg.DeliveryLimits = []int{3}
	lean, err := x.EvaluateChoices(context.Background(), q, js, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(lean.Selections) != 0 || lean.ContinuousChoices != nil || lean.Choices == nil || !lean.Choices.Complete || len(lean.Choices.Groups[0].Selections) != 1 {
		t.Fatalf("unrequested output leaked: %+v", lean)
	}
	if !reflect.DeepEqual(lean.Choices.Groups[0].Selections[0], legacy.Choices.Groups[0].Selections[1]) {
		t.Fatalf("lean changed finite selection")
	}
	for _, f := range lean.Features {
		for _, tr := range f.Transfers {
			if len(tr.Recovery) != 0 {
				t.Fatal("lean recovery enumeration leaked")
			}
		}
	}
	cfg = ChoiceConfig{MaxCandidates: 100, LeanDelivery: true, DeliveryPolicy: "pattern_anchors_v1", DeliveryLimits: []int{3, 5}}
	lean, err = x.EvaluateChoices(context.Background(), q, js, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if lean.Choices != nil || len(lean.Selections) != 2 || !reflect.DeepEqual(lean.Selections, legacy.Selections[2:4]) {
		t.Fatal("lean changed requested pattern outputs")
	}
}

func TestSharedCheckerAdaptersKeepSnapshotIdentityAndNoRecovery(t *testing.T) {
	d, q, js := runtimeFixture()
	checker, err := validate.New(d)
	if err != nil {
		t.Fatal(err)
	}
	x, err := NewBoundedFromChecker(checker)
	if err != nil {
		t.Fatal(err)
	}
	d.Connections[0].Arrival = 200
	d.Trips[0].RouteID = "mutated"
	bounded := boundedConfig(true)
	bounded.Bounded.Limits = []int{3}
	legacy := legacyRuntimeConfig()
	legacy.LeanDelivery = true
	legacy.DeliveryLimits = []int{3}
	for _, cfg := range []ChoiceConfig{bounded, legacy, runtimeSafeConfig("absolute"), runtimeSafeConfig("normalized"), runtimeFillConfig(3), runtimeFillConfig(5)} {
		r, err := x.EvaluateChoices(context.Background(), q, append(js, js[0]), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Features) != len(js) || r.FeatureMode != "validated_objective_pattern_slack_no_recovery" || len(r.Selections) != 0 {
			t.Fatalf("adapter did not retain native identities exactly once: %+v", r)
		}
		by := map[string]Feature{}
		for _, f := range r.Features {
			by[f.Identity] = f
			for _, tr := range f.Transfers {
				if len(tr.Recovery) != 0 {
					t.Fatal("recovery enumeration leaked")
				}
			}
		}
		if f, ok := by[model.Identity(js[0])]; !ok || f.Duration != 100 || f.Pattern != "[{\"Kind\":\"ride\",\"Route\":\"r-direct\",\"Stops\":[\"a\",\"d\"],\"Walk\":\"\"}]" {
			t.Fatalf("snapshot alias or identity drift: %+v", f)
		}
		if cfg.Bounded != nil && (r.ContinuousChoices == nil || !r.ContinuousChoices.Complete || len(r.ContinuousChoices.Selections) != 1 || r.ContinuousChoices.Selections[0].Limit != 3) {
			t.Fatal("bounded dispatch changed request")
		}
		if cfg.FiniteSafe != nil && (r.FiniteSafeChoices == nil || !r.FiniteSafeChoices.Complete || r.Choices != nil || r.ContinuousChoices != nil) {
			t.Fatal("safe dispatch leaked controls")
		}
		if cfg.HybridFill != nil {
			if r.HybridFillChoices == nil || !r.HybridFillChoices.Complete || r.Choices != nil || r.ContinuousChoices != nil || r.FiniteSafeChoices != nil {
				t.Fatal("fill dispatch leaked controls")
			}
			s := r.HybridFillChoices.Selection
			if s.Limit != cfg.DeliveryLimits[0] || len(s.Selected) != 3 || !s.SetInclusionVerified || s.FilledSubsetOptimalClaim {
				t.Fatalf("fill cardinality/identity scope: %+v", s)
			}
		}
	}
	if _, err = x.Evaluate(q, js); err == nil {
		t.Fatal("shared checker index accepted legacy recovery evaluation")
	}
	if _, err = NewBoundedFromChecker(nil); err == nil {
		t.Fatal("nil checker accepted")
	}
	if _, err = NewBoundedFromChecker(&validate.Checker{}); err == nil {
		t.Fatal("uninitialized checker accepted")
	}
	original, _, _ := runtimeFixture()
	full, err := New(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []ChoiceConfig{runtimeSafeConfig("absolute"), runtimeFillConfig(3)} {
		if _, err = full.EvaluateChoices(context.Background(), q, js, cfg); err == nil {
			t.Fatal("new policy silently used recovery index")
		}
	}
}

func TestLeanEntryPointRejectsMixedConfigsAndHonorsEmptyCancellation(t *testing.T) {
	d, q, _ := runtimeFixture()
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []ChoiceConfig{legacyRuntimeConfig(), boundedConfig(true), runtimeSafeConfig("absolute"), runtimeFillConfig(3)} {
		if _, err = x.EvaluateLeanChoices(context.Background(), q, nil, cfg); err == nil {
			t.Fatal("direct lean entry accepted another policy")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, policy := range []string{"finite_preferences_v2", "pattern_anchors_v1"} {
		cfg := legacyRuntimeConfig()
		cfg.LeanDelivery = true
		cfg.DeliveryPolicy = policy
		r, err := x.EvaluateChoices(ctx, q, nil, cfg)
		if !errors.Is(err, context.Canceled) || (r.Choices != nil && r.Choices.Complete) {
			t.Fatalf("empty lean cancellation ignored: %+v %v", r, err)
		}
	}
}

func TestOptInCandidateBudgetsAndWitnessValidation(t *testing.T) {
	d, q, js := runtimeFixture()
	checker, err := validate.New(d)
	if err != nil {
		t.Fatal(err)
	}
	x, err := NewBoundedFromChecker(checker)
	if err != nil {
		t.Fatal(err)
	}
	bounded := boundedConfig(true)
	bounded.MaxCandidates = 1
	lean := legacyRuntimeConfig()
	lean.LeanDelivery = true
	lean.MaxCandidates = 1
	for _, cfg := range []ChoiceConfig{bounded, lean} {
		r, err := x.EvaluateChoices(context.Background(), q, js, cfg)
		if !errors.Is(err, ErrSelectionBudget) || (r.Choices != nil && r.Choices.Complete) || (r.ContinuousChoices != nil && r.ContinuousChoices.Complete) {
			t.Fatalf("candidate guard falsely complete: %+v %v", r, err)
		}
	}
	bad := append([]model.Journey{}, js...)
	bad[0] = model.Journey{Legs: append([]model.Leg{}, js[0].Legs...)}
	bad[0].Legs[0].Arrival++
	for _, cfg := range []ChoiceConfig{boundedConfig(true), runtimeSafeConfig("absolute"), runtimeFillConfig(3)} {
		if _, err = x.EvaluateChoices(context.Background(), q, bad, cfg); err == nil {
			t.Fatal("invalid witness reached selector")
		}
	}
}

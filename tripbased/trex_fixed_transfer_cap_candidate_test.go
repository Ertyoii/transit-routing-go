package tripbased

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func fixedRankLiteral(t *testing.T) (model.Dataset, model.Query, model.Journey, TREXConfig) {
	t.Helper()
	raw, e := os.ReadFile("testdata/trex_fixed_transfer_cap/rank-loss.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Dataset   model.Dataset  `json:"dataset"`
		Query     model.Query    `json:"query"`
		Expected  model.Journey  `json:"expected"`
		Partition map[string]int `json:"partition"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	cfg := syntheticCapConfig(1)
	cfg.Partition = fixture.Partition
	return fixture.Dataset, fixture.Query, fixture.Expected, cfg
}
func fixedCapPrepare(t *testing.T, index *TREXIndex, limits TREXStaticCapVerifyLimits) *TREXFixedTransferCapCandidate {
	t.Helper()
	c, stats, e := PrepareTREXFixedTransferCapCandidate(context.Background(), index, limits, TREXRawVerifierPreparationLimits{MaxRows: 1000, MaxWork: 2000, MaxPayloadBytes: 100000})
	if e != nil || stats.OrderedSHA256 == "" {
		t.Fatal("raw preparation incomplete", stats, e)
	}
	return c
}
func TestTREXFixedTransferCapRankRecovery(t *testing.T) {
	d, q, want, cfg := fixedRankLiteral(t)
	ctx := context.Background()
	old, e := BuildTREXStaticRulesCandidate(ctx, d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	oldGate, e := PrepareTREXRoleCapCandidate(ctx, old, syntheticCapVerifyLimits())
	if e != nil {
		t.Fatal(e)
	}
	declined, e := oldGate.Search(ctx, q)
	if e == nil || declined.DeclineReason != "frontier_mismatch" || !declined.VerificationComplete || len(declined.Journeys) != 0 {
		t.Fatal("old rank-loss control changed", declined, e)
	}
	index, e := BuildTREXFixedTransferCapRankedCandidate(ctx, d, cfg, 900)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := index.Snapshot()
	diagnostic := index.Diagnostic()
	if len(snapshot.Transfers) != 2 || snapshot.FixedTransferWalkingCap == nil || *snapshot.FixedTransferWalkingCap != 900 || diagnostic.FixedTransferWalkingCap == nil || *diagnostic.FixedTransferWalkingCap != 900 {
		t.Fatal("fixed graph/config binding", snapshot, diagnostic)
	}
	for _, edge := range snapshot.Transfers {
		if edge.Rank != 1 || edge.WalkSeconds != 0 {
			t.Fatal("valid transfer not promoted", edge)
		}
	}
	*diagnostic.FixedTransferWalkingCap = 12
	if *index.Diagnostic().FixedTransferWalkingCap != 900 {
		t.Fatal("diagnostic cap aliases prepared policy")
	}
	core, e := index.searchTREXForCertificate(ctx, q, "overlay-role-caps", true)
	if e != nil || !reflect.DeepEqual(core.Journeys, []model.Journey{want}) {
		t.Fatal("active ranked kernel did not recover literal word", core, e)
	}
	gate := fixedCapPrepare(t, index, syntheticCapVerifyLimits())
	before, _ := json.Marshal(q)
	got, e := gate.Search(ctx, q)
	if e != nil || !got.Complete || !got.Certificate.Accepted || !got.VerificationComplete || got.Certificate.Method != TREXFixedTransferCapCandidateMethod || got.Certificate.CheckedRepresentatives != 1 || !reflect.DeepEqual(got.Journeys, core.Journeys) {
		t.Fatal("fixed-cap certification", got, e)
	}
	if got.VerificationProbes != got.VerifierStats.Probes+got.ProjectionScans || got.VerifierStats.Policy == "" {
		t.Fatal("new cost policy hidden", got)
	}
	after, _ := json.Marshal(q)
	if string(before) != string(after) {
		t.Fatal("query changed")
	}
	for _, cap := range []int64{899, 901} {
		t.Run("incompatible_cap_"+string(rune('0'+cap-899)), func(t *testing.T) {
			other := q
			w := *q.Walking
			w.Transfer = cap
			other.Walking = &w
			bad, e := gate.Search(ctx, other)
			if e == nil || bad.DeclineReason != "fixed_transfer_cap_mismatch" || bad.Complete || bad.Certificate.Accepted || len(bad.Journeys) != 0 {
				t.Fatal("mismatched rank policy accepted", bad, e)
			}
		})
	}
	if _, _, e = PrepareTREXFixedTransferCapCandidate(ctx, old, syntheticCapVerifyLimits(), TREXRawVerifierPreparationLimits{MaxRows: 100, MaxWork: 100, MaxPayloadBytes: 10000}); e == nil {
		t.Fatal("unrestricted index mislabeled fixed-cap")
	}
}

func TestTREXFixedTransferCapOriginalLiteralFixtures(t *testing.T) {
	for _, fixture := range syntheticCapCases(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			index, e := BuildTREXFixedTransferCapRankedCandidate(context.Background(), fixture.Dataset, syntheticCapConfig(fixture.Levels), fixture.Query.Walking.Transfer)
			if e != nil {
				t.Fatal(e)
			}
			c := fixedCapPrepare(t, index, syntheticCapVerifyLimits())
			got, e := c.Search(context.Background(), fixture.Query)
			if e != nil || !got.Certificate.Accepted || !reflect.DeepEqual(syntheticCapRefs(got.Journeys), fixture.FeasibleRefs) {
				t.Fatal("original literal recovery", got, e)
			}
		})
	}
}

func TestTREXFixedTransferCapFailClosed(t *testing.T) {
	d, q, _, cfg := fixedRankLiteral(t)
	for _, kind := range []string{"missing_ranked_links", "ranked_work", "replay_work", "actual_probes", "states", "projection", "cancelled", "aggregate", "legs"} {
		t.Run(kind, func(t *testing.T) {
			config, limits := cfg, syntheticCapVerifyLimits()
			index, e := BuildTREXFixedTransferCapRankedCandidate(context.Background(), d, config, 900)
			if e != nil {
				t.Fatal(e)
			}
			gate := fixedCapPrepare(t, index, limits)
			if kind == "ranked_work" {
				index.cfg.Limits.MaxQueryWork = 1
			}
			if kind == "missing_ranked_links" {
				for i := range index.overlays {
					index.overlays[i].edges = nil
					for j := range index.overlays[i].starts {
						index.overlays[i].starts[j] = 0
					}
				}
			}
			if kind == "replay_work" || kind == "actual_probes" || kind == "projection" {
				complete, e := gate.Search(context.Background(), q)
				if e != nil {
					t.Fatal(e)
				}
				switch kind {
				case "replay_work":
					index.cfg.Limits.MaxQueryWork = complete.Stats.Work - 1
				case "actual_probes":
					gate.gate.limits.MaxScans = complete.VerifierStats.Probes - 1
				case "projection":
					gate.gate.limits.MaxScans = complete.VerificationProbes - 1
				}
			}
			if kind == "states" {
				gate.gate.limits.MaxStates = 1
			}
			query := q
			if kind == "aggregate" {
				query.MaxWalkSeconds = 0
			}
			if kind == "legs" {
				query.MaxLegs = 1
			}
			ctx := context.Background()
			if kind == "cancelled" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			got, e := gate.Search(ctx, query)
			if e == nil || got.Complete || got.Certificate.Accepted || len(got.Journeys) != 0 {
				t.Fatal("incomplete/infeasible path escaped", kind, got, e)
			}
			if kind == "missing_ranked_links" && (!got.VerificationComplete || got.DeclineReason != "frontier_mismatch") {
				t.Fatal("rank omissions must remain visible", got)
			}
		})
	}
}

package ultra

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

var sparseCostSink SparseCostSummary

func compareSparseCost(t *testing.T, full SparsePreparation, cost SparseCostSummary) {
	t.Helper()
	f, c := reflect.ValueOf(full), reflect.ValueOf(cost)
	for i := 0; i < f.NumField(); i++ {
		name := f.Type().Field(i).Name
		if name == "Certificates" {
			continue
		}
		field := c.FieldByName(name)
		if !field.IsValid() || !reflect.DeepEqual(f.Field(i).Interface(), field.Interface()) {
			t.Fatalf("scalar field omitted/changed:%s", name)
		}
	}
	proofs := 0
	for _, v := range full.Certificates {
		proofs += len(v.Proofs)
	}
	if cost.CertificateCount != len(full.Certificates) || cost.ProofRecordCount != proofs || cost.CertificatesExported || cost.ProofRecordsExported {
		t.Fatal("count/export mismatch", cost)
	}
}
func TestSparseCostScalarShapeCountsAndOwnership(t *testing.T) {
	x := &SparseIndex{prep: SparsePreparation{Complete: true, Profile: "literal", RetainedPairs: 2, PrunedPairs: 2, ProofChecks: 19,
		Certificates: []PrunedTransfer{{From: "a", To: "b", Proofs: []StayProof{{Incoming: "c0"}, {Incoming: "c1"}}}, {From: "b", To: "c", Proofs: []StayProof{{Incoming: "c2"}}}}}}
	before := x.Preparation()
	s := x.CostSummary()
	compareSparseCost(t, before, s)
	var scalarOnly func(reflect.Type)
	scalarOnly = func(v reflect.Type) {
		switch v.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Func:
			t.Fatalf("non-scalar-owned summary field:%v", v)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				scalarOnly(v.Field(i).Type)
			}
		}
	}
	scalarOnly(reflect.TypeOf(s))
	s.Config.MaxProofChecks = 0
	s.Control.Stats.Runs = -1
	if !reflect.DeepEqual(before, x.Preparation()) {
		t.Fatal("owned summary changed native preparation")
	}
	owned := x.Preparation()
	owned.Certificates[0].From = "changed"
	owned.Certificates[0].Proofs[0].Incoming = "changed"
	if !reflect.DeepEqual(before, x.Preparation()) {
		t.Fatal("original Preparation ownership changed")
	}
	if n := testing.AllocsPerRun(10, func() { sparseCostSink = x.CostSummary() }); n != 0 {
		t.Fatal("scalar summary allocated", n)
	}
	b, e := json.Marshal(x.CostSummary())
	if e != nil {
		t.Fatal(e)
	}
	var shape map[string]any
	json.Unmarshal(b, &shape)
	if _, ok := shape["certificates"]; ok {
		t.Fatal("certificate array exported")
	}
	if _, ok := shape["proofs"]; ok {
		t.Fatal("proof array exported")
	}
}
func TestSparseCostNativePartialCountParity(t *testing.T) {
	for seed := uint64(0); seed < 24; seed++ {
		for _, budget := range []int{1, 2, 10, 100, 1000000} {
			cfg := SparseConfig{Capped: CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()}, TransferBuffer: 0, MaxProofChecks: budget}
			x, _ := BuildSparse(context.Background(), denseDataset(seed), cfg)
			before := x.Preparation()
			compareSparseCost(t, before, x.CostSummary())
			if !reflect.DeepEqual(before, x.Preparation()) {
				t.Fatal("summary changed native partial/success state")
			}
		}
	}
}
func TestSparseCostConcurrentNativeSearchAndFullEvidence(t *testing.T) {
	d := denseDataset(1)
	cfg := SparseConfig{Capped: CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()}, TransferBuffer: 0, MaxProofChecks: 1000000}
	x, e := BuildSparse(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	q := denseQuery("s0", "s4", 0, 2, false)
	q.TransferBuffer = 0
	want, we := x.Search(context.Background(), q, denseLimits())
	full := x.Preparation()
	summary := x.CostSummary()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if !reflect.DeepEqual(summary, x.CostSummary()) || !reflect.DeepEqual(full, x.Preparation()) {
					t.Error("concurrent diagnostics mutation")
				}
				got, ge := x.Search(context.Background(), q, denseLimits())
				if denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
					t.Error("diagnostics changed native search")
				}
			}
		}()
	}
	wg.Wait()
	var nilIndex *SparseIndex
	if nilIndex.CostSummary().Complete {
		t.Fatal("nil summary complete")
	}
}

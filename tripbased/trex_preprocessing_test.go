package tripbased_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"github.com/ertyoii/transit-routing-go/validate"
)

type preprocessingFixture struct {
	Name            string                `json:"name"`
	Dataset         model.Dataset         `json:"dataset"`
	Query           model.Query           `json:"query"`
	Partition       map[string]int        `json:"partition"`
	ExpectedWitness tripbased.TREXWitness `json:"expected_witness"`
}

func preprocessingFixtures(t *testing.T) []preprocessingFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/trex_preprocessing/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schema   string                 `json:"schema"`
		License  string                 `json:"license"`
		Fixtures []preprocessingFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "trex-preprocessing-literal-v1" || document.License != "CC0-1.0" || len(document.Fixtures) != 2 {
		t.Fatal("unexpected fixture binding")
	}
	return document.Fixtures
}

func preprocessingConfig(f preprocessingFixture, exact bool) tripbased.TREXConfig {
	partition := map[string]int{}
	for k, v := range f.Partition {
		partition[k] = v
	}
	return tripbased.TREXConfig{ExactStorage: exact, Levels: 1, MaxBoardings: 3, Partition: partition, Limits: tripbased.TREXLimits{
		MaxStops: 100, MaxTrips: 100, MaxInputEdges: 1000, MaxClosurePaths: 1000, MaxWalkSettled: 1000, MaxTransfers: 1000,
		MaxRuns: 1000, MaxWork: 100000, MaxWitnesses: 1000, MaxOverlayEntries: 10000, MaxQueuedSegments: 10000, MaxQueryWork: 100000,
	}}
}

func TestTREXPreprocessingLiteralReplayAndOwnership(t *testing.T) {
	for _, fixture := range preprocessingFixtures(t) {
		for _, exact := range []bool{false, true} {
			name := fixture.Name + "/append"
			if exact {
				name = fixture.Name + "/exact"
			}
			t.Run(name, func(t *testing.T) {
				dataset := fixture.Dataset.Clone()
				config := preprocessingConfig(fixture, exact)
				index, err := tripbased.BuildTREXStaticRulesCandidate(context.Background(), dataset, config)
				if err != nil || !index.Complete() {
					t.Fatal("tiny construction incomplete", err)
				}
				snapshot := index.Snapshot()
				want := []tripbased.TREXWitness{fixture.ExpectedWitness}
				if !reflect.DeepEqual(snapshot.Witnesses, want) {
					t.Fatalf("literal ordered proof mismatch: got %+v want %+v", snapshot.Witnesses, want)
				}
				// Whole-word replay is the independently authored specification from
				// independent_spec_test.go; it imports no router or pruning helper.
				for _, witness := range snapshot.Witnesses {
					word := []int{}
					for _, leg := range witness.Journey.Legs {
						found := -1
						for i, connection := range fixture.Dataset.Connections {
							if leg.Kind == "ride" && leg.Ref == connection.ID {
								found = i
							}
						}
						for i, walk := range fixture.Dataset.Footpaths {
							if leg.Kind == "walk" && leg.Ref == walk.ID {
								found = len(fixture.Dataset.Connections) + i
							}
						}
						if found < 0 {
							t.Fatal("unknown raw leg", leg)
						}
						word = append(word, found)
					}
					replayed, metrics, valid := assess(fixture.Dataset, fixture.Query, word)
					if !valid || !reflect.DeepEqual(replayed, witness.Journey) || metrics.Boardings != len(witness.Transfers)+1 {
						t.Fatal("independent full witness replay failed", replayed, metrics, valid)
					}
					checker, err := validate.New(fixture.Dataset)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := checker.Check(fixture.Query, witness.Journey); err != nil {
						t.Fatal("production whole-witness validation failed", err)
					}
				}
				// Caller inputs and returned views must stay owned after preparation.
				dataset.Connections[1].ID = "caller-mutated"
				dataset.Footpaths[0].Duration = 999
				config.Partition["C"] = 0
				snapshot.Witnesses[0].Transfers[0] = 999
				snapshot.Witnesses[0].Journey.Legs[0].Ref = "view-mutated"
				var group sync.WaitGroup
				for i := 0; i < 4; i++ {
					group.Add(1)
					go func() {
						defer group.Done()
						for n := 0; n < 4; n++ {
							if !reflect.DeepEqual(index.Snapshot().Witnesses, want) || index.Diagnostic().RetainedWitnesses != 1 {
								t.Error("owned concurrent proof expansion changed")
							}
						}
					}()
				}
				group.Wait()
			})
		}
	}
}

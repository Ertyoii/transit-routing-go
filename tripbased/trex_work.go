package tripbased

// Bounded work telemetry, one row per entered level and two event exemplars.
// These values never participate in search/pruning or resource guards.
type TREXEventWork struct {
	Trip       string `json:"source_trip"`
	Board      int    `json:"source_board"`
	Work       int    `json:"work"`
	Queued     int    `json:"queued"`
	Promotions int    `json:"promotions"`
	Complete   bool   `json:"complete"`
}
type TREXLevelWork struct {
	Level          int           `json:"level"`
	Complete       bool          `json:"complete"`
	Started        int           `json:"event_runs_started"`
	Completed      int           `json:"event_runs_completed"`
	Work           int           `json:"work"`
	Queued         int           `json:"queued"`
	Promotions     int           `json:"promotions"`
	Traversals     int           `json:"traversals"`
	Redundant      int           `json:"redundant_witnesses"`
	ElapsedNS      int64         `json:"elapsed_ns"`
	AllocatedBytes uint64        `json:"allocated_bytes"`
	Last           TREXEventWork `json:"last_event"`
	MaxWork        TREXEventWork `json:"maximum_work_event"`
}

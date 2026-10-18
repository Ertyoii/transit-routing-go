// Package gtfs normalizes a deliberately limited static GTFS profile.
// Frequency-based service is rejected unless an explicitly labeled model expansion is requested.
package gtfs

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	ServiceDate     string `json:"service_date"`
	Start           int64  `json:"start"`
	End             int64  `json:"end"`
	FrequencyPolicy string `json:"frequency_policy"`
	TransferEdges   string `json:"transfer_edges"`
	DataKind        string `json:"data_kind"`
	MaxConnections  int    `json:"max_connections"`
}
type Audit struct {
	Version               string         `json:"importer_version"`
	RawSHA256             string         `json:"raw_sha256"`
	NormalizedSHA256      string         `json:"normalized_sha256"`
	Options               Options        `json:"options"`
	FileRows              map[string]int `json:"file_rows"`
	IgnoredFiles          []string       `json:"ignored_files"`
	FrequencyRows         int            `json:"frequency_rows"`
	HeadwayRows           int            `json:"headway_rows"`
	ExactFrequencyRows    int            `json:"exact_frequency_rows"`
	EstimatedStopTimes    int            `json:"estimated_stop_times"`
	InactiveTransferRules int            `json:"inactive_transfer_rules"`
	InstanceCount         int            `json:"instance_count"`
	ConnectionCount       int            `json:"connection_count"`
	FootpathCount         int            `json:"footpath_count"`
	Notes                 []string       `json:"notes"`
}
type row map[string]string
type table struct {
	fields map[string]bool
	rows   []row
}

func decodeTables(data []byte) (map[string]table, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	tables := map[string]table{}
	var total uint64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			return nil, fmt.Errorf("GTFS ZIP must have flat files")
		}
		if path.Base(f.Name) != f.Name || strings.Contains(f.Name, "\\") || f.Name == "." {
			return nil, fmt.Errorf("invalid ZIP entry name")
		}
		if _, ok := tables[f.Name]; ok {
			return nil, fmt.Errorf("duplicate ZIP entry")
		}
		total += f.UncompressedSize64
		if f.UncompressedSize64 > 32<<20 || total > 256<<20 {
			return nil, fmt.Errorf("GTFS uncompressed limit exceeded")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, 32<<20+1))
		closeErr := r.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(b) > 32<<20 {
			return nil, fmt.Errorf("table too large")
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(b), "\ufeff")))
		header, e := reader.Read()
		if e != nil {
			return nil, fmt.Errorf("%s header: %w", f.Name, e)
		}
		t := table{fields: map[string]bool{}, rows: []row{}}
		for _, h := range header {
			if h == "" || strings.TrimSpace(h) != h || t.fields[h] {
				return nil, fmt.Errorf("%s invalid/duplicate header", f.Name)
			}
			t.fields[h] = true
		}
		for {
			cells, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, e)
			}
			v := row{}
			for i, h := range header {
				v[h] = cells[i]
			}
			t.rows = append(t.rows, v)
		}
		tables[f.Name] = t
	}
	return tables, nil
}
func required(ts map[string]table, name string, fields ...string) ([]row, error) {
	t, ok := ts[name]
	if !ok {
		return nil, fmt.Errorf("missing %s", name)
	}
	for _, f := range fields {
		if !t.fields[f] {
			return nil, fmt.Errorf("%s missing column %s", name, f)
		}
	}
	return t.rows, nil
}
func number(s string, defaultValue int64) (int64, error) {
	if s == "" {
		return defaultValue, nil
	}
	return strconv.ParseInt(s, 10, 64)
}
func Time(s string) (int64, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, fmt.Errorf("invalid GTFS time %q", s)
	}
	nums := [3]int64{}
	for i, p := range parts {
		for _, digit := range p {
			if digit < '0' || digit > '9' {
				return 0, fmt.Errorf("invalid time digit")
			}
		}
		v, e := strconv.ParseInt(p, 10, 64)
		if e != nil || v < 0 {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		nums[i] = v
	}
	if nums[0] > 168 || nums[1] > 59 || nums[2] > 59 {
		return 0, fmt.Errorf("time outside supported range")
	}
	n := nums[0]*3600 + nums[1]*60 + nums[2]
	if n > model.MaxServiceSecond {
		return 0, fmt.Errorf("time outside supported range")
	}
	return n, nil
}
func access(s string) (model.Access, error) {
	switch s {
	case "", "0":
		return model.Unknown, nil
	case "1":
		return model.Yes, nil
	case "2":
		return model.No, nil
	}
	return "", fmt.Errorf("invalid accessibility value")
}
func pickup(s string) (bool, error) {
	switch s {
	case "", "0":
		return true, nil
	case "1":
		return false, nil
	}
	return false, fmt.Errorf("unsupported pickup/dropoff type %q", s)
}
func date(s string) (time.Time, error) {
	t, e := time.Parse("20060102", s)
	if e != nil || t.Format("20060102") != s {
		return t, fmt.Errorf("invalid GTFS date %q", s)
	}
	return t, nil
}
func hash(b []byte) string          { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func stableID(values ...any) string { b, _ := json.Marshal(values); return string(b) }

type stopTime struct {
	stop               string
	seq                int64
	arrival, departure int64
	pickup, dropoff    bool
}
type frequency struct {
	start, end, headway int64
	exact               bool
}
type template struct {
	id, route, service string
	accessible         model.Access
	times              []stopTime
	freq               []frequency
}
type calendar struct {
	start, end time.Time
	week       [7]bool
}

func Import(data []byte, opt Options) (model.Dataset, Audit, error) {
	audit := Audit{Version: "gtfs-static-0.2", RawSHA256: hash(data), Options: opt, FileRows: map[string]int{}, IgnoredFiles: []string{}, Notes: []string{}}
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "gtfs-" + audit.RawSHA256[:16], ServiceDate: opt.ServiceDate, DataKind: opt.DataKind, Window: &model.TimeWindow{Start: opt.Start, End: opt.End}, Stops: []model.Stop{}, Trips: []model.Trip{}, Connections: []model.Connection{}, Footpaths: []model.Footpath{}}
	fail := func(e error) (model.Dataset, Audit, error) { return model.Dataset{}, audit, e }
	base, e := time.Parse("2006-01-02", opt.ServiceDate)
	if e != nil || base.Format("2006-01-02") != opt.ServiceDate {
		return fail(fmt.Errorf("invalid service date"))
	}
	if opt.Start < 0 || opt.End < opt.Start || opt.End > model.MaxServiceSecond || opt.MaxConnections <= 0 {
		return fail(fmt.Errorf("explicit finite window and connection budget required"))
	}
	if opt.FrequencyPolicy != "reject" && opt.FrequencyPolicy != "deterministic_model" {
		return fail(fmt.Errorf("unknown frequency policy"))
	}
	if opt.TransferEdges != "none" && opt.TransferEdges != "minimum_time_proxy" {
		return fail(fmt.Errorf("unknown transfer-edge policy"))
	}
	if opt.DataKind != "scheduled_snapshot" && opt.DataKind != "modeled_snapshot" {
		return fail(fmt.Errorf("explicit data kind required"))
	}
	ts, e := decodeTables(data)
	if e != nil {
		return fail(e)
	}
	allowed := map[string]bool{}
	for _, n := range []string{"agency.txt", "stops.txt", "routes.txt", "trips.txt", "stop_times.txt", "calendar.txt", "calendar_dates.txt", "frequencies.txt", "transfers.txt"} {
		allowed[n] = true
	}
	ignored := map[string]bool{}
	for _, n := range []string{"feed_info.txt", "attributions.txt", "shapes.txt", "translations.txt", "fare_attributes.txt", "fare_rules.txt"} {
		ignored[n] = true
	}
	for name, t := range ts {
		audit.FileRows[name] = len(t.rows)
		if !allowed[name] {
			if !ignored[name] && len(t.rows) > 0 {
				return fail(fmt.Errorf("unsupported GTFS table %s", name))
			}
			audit.IgnoredFiles = append(audit.IgnoredFiles, name)
		}
	}
	sort.Strings(audit.IgnoredFiles)
	agencies, e := required(ts, "agency.txt", "agency_timezone")
	if e != nil {
		return fail(e)
	}
	if len(agencies) == 0 {
		return fail(fmt.Errorf("no agency"))
	}
	for _, a := range agencies {
		if d.TimeZone != "" && d.TimeZone != a["agency_timezone"] {
			return fail(fmt.Errorf("multiple agency timezones unsupported"))
		}
		d.TimeZone = a["agency_timezone"]
	}
	loc, e := time.LoadLocation(d.TimeZone)
	if e != nil || d.TimeZone == "" || d.TimeZone == "Local" {
		return fail(fmt.Errorf("invalid agency timezone"))
	}
	// This milestone handles Asia/Singapore only; no untested DST conversion is implied.
	if d.TimeZone != "Asia/Singapore" {
		return fail(fmt.Errorf("import profile currently supports Asia/Singapore only"))
	}
	_ = loc
	stops, e := required(ts, "stops.txt", "stop_id")
	if e != nil {
		return fail(e)
	}
	stopMap := map[string]model.Stop{}
	for _, s := range stops {
		id := s["stop_id"]
		// Station-entrance/pathway access is outside this import profile.
		// Reject explicit policies instead of silently permitting street access.
		if s["stop_access"] != "" {
			return fail(fmt.Errorf("unsupported stops.stop_access=%q at stop %q; station entrance/pathway access is not modeled", s["stop_access"], id))
		}
		if id == "" {
			return fail(fmt.Errorf("empty stop ID"))
		}
		if _, ok := stopMap[id]; ok {
			return fail(fmt.Errorf("duplicate stop ID"))
		}
		lt, e := number(s["location_type"], 0)
		if e != nil || lt < 0 || lt > 1 {
			return fail(fmt.Errorf("unsupported stop location type"))
		}
		a, e := access(s["wheelchair_boarding"])
		if e != nil {
			return fail(e)
		}
		stopMap[id] = model.Stop{ID: id, ParentStation: s["parent_station"], LocationType: int(lt), Accessible: a}
	}
	for _, s := range stops {
		v := stopMap[s["stop_id"]]
		if v.ParentStation != "" {
			p, ok := stopMap[v.ParentStation]
			if !ok || p.LocationType != 1 || p.ParentStation != "" {
				return fail(fmt.Errorf("invalid station parent"))
			}
			if v.Accessible == model.Unknown {
				v.Accessible = p.Accessible
			}
		}
		d.Stops = append(d.Stops, v)
	}
	routes, e := required(ts, "routes.txt", "route_id")
	if e != nil {
		return fail(e)
	}
	routeIDs := map[string]bool{}
	for _, r := range routes {
		if r["route_id"] == "" || routeIDs[r["route_id"]] {
			return fail(fmt.Errorf("duplicate/empty route ID"))
		}
		routeIDs[r["route_id"]] = true
		for _, f := range []string{"continuous_pickup", "continuous_drop_off"} {
			if r[f] != "" && r[f] != "1" {
				return fail(fmt.Errorf("continuous service unsupported"))
			}
		}
	}
	trips, e := required(ts, "trips.txt", "trip_id", "route_id", "service_id")
	if e != nil {
		return fail(e)
	}
	templates := map[string]*template{}
	order := []string{}
	for _, t := range trips {
		if t["trip_id"] == "" || templates[t["trip_id"]] != nil || !routeIDs[t["route_id"]] || t["service_id"] == "" {
			return fail(fmt.Errorf("invalid/duplicate trip"))
		}
		if t["block_id"] != "" {
			return fail(fmt.Errorf("block/in-seat semantics unsupported"))
		}
		a, e := access(t["wheelchair_accessible"])
		if e != nil {
			return fail(e)
		}
		templates[t["trip_id"]] = &template{id: t["trip_id"], route: t["route_id"], service: t["service_id"], accessible: a}
		order = append(order, t["trip_id"])
	}
	sort.Strings(order)
	times, e := required(ts, "stop_times.txt", "trip_id", "stop_id", "stop_sequence", "arrival_time", "departure_time")
	if e != nil {
		return fail(e)
	}
	var maxSpan int64
	for _, r := range times {
		t := templates[r["trip_id"]]
		s, ok := stopMap[r["stop_id"]]
		if t == nil || !ok || s.LocationType != 0 {
			return fail(fmt.Errorf("invalid stop-time reference"))
		}
		for _, f := range []string{"location_group_id", "location_id", "start_pickup_drop_off_window", "end_pickup_drop_off_window", "pickup_booking_rule_id", "drop_off_booking_rule_id"} {
			if r[f] != "" {
				return fail(fmt.Errorf("flex/booking stop times unsupported"))
			}
		}
		for _, f := range []string{"continuous_pickup", "continuous_drop_off"} {
			if r[f] != "" && r[f] != "1" {
				return fail(fmt.Errorf("continuous stop times unsupported"))
			}
		}
		ar, e := Time(r["arrival_time"])
		if e != nil {
			return fail(e)
		}
		dep, e := Time(r["departure_time"])
		if e != nil || dep < ar {
			return fail(fmt.Errorf("invalid stop event times"))
		}
		seq, e := number(r["stop_sequence"], -1)
		if e != nil || seq < 0 {
			return fail(fmt.Errorf("invalid stop sequence"))
		}
		p, e := pickup(r["pickup_type"])
		if e != nil {
			return fail(e)
		}
		drop, e := pickup(r["drop_off_type"])
		if e != nil {
			return fail(e)
		}
		if r["timepoint"] == "0" {
			audit.EstimatedStopTimes++
		} else if r["timepoint"] != "" && r["timepoint"] != "1" {
			return fail(fmt.Errorf("invalid timepoint"))
		}
		t.times = append(t.times, stopTime{s.ID, seq, ar, dep, p, drop})
		maxSpan = max(maxSpan, dep)
	}
	for _, t := range templates {
		sort.Slice(t.times, func(i, j int) bool { return t.times[i].seq < t.times[j].seq })
		if len(t.times) < 2 {
			return fail(fmt.Errorf("trip %s has fewer than two events", t.id))
		}
		for i := 1; i < len(t.times); i++ {
			if t.times[i].seq == t.times[i-1].seq || t.times[i].arrival < t.times[i-1].departure {
				return fail(fmt.Errorf("invalid chain in trip %s", t.id))
			}
		}
	}
	if _, ok := ts["frequencies.txt"]; ok {
		fs, e := required(ts, "frequencies.txt", "trip_id", "start_time", "end_time", "headway_secs")
		if e != nil {
			return fail(e)
		}
		for _, r := range fs {
			t := templates[r["trip_id"]]
			if t == nil {
				return fail(fmt.Errorf("frequency trip missing"))
			}
			start, e := Time(r["start_time"])
			if e != nil {
				return fail(e)
			}
			end, e := Time(r["end_time"])
			if e != nil || end <= start {
				return fail(fmt.Errorf("invalid frequency interval"))
			}
			h, e := number(r["headway_secs"], 0)
			if e != nil || h <= 0 || h > model.MaxServiceSecond {
				return fail(fmt.Errorf("invalid headway"))
			}
			exact := r["exact_times"] == "1"
			if !exact && r["exact_times"] != "" && r["exact_times"] != "0" {
				return fail(fmt.Errorf("invalid exact_times"))
			}
			audit.FrequencyRows++
			if exact {
				audit.ExactFrequencyRows++
			} else {
				audit.HeadwayRows++
				if opt.FrequencyPolicy != "deterministic_model" || opt.DataKind != "modeled_snapshot" {
					return fail(fmt.Errorf("headway service requires explicit deterministic_model and modeled_snapshot; exact departures unavailable"))
				}
			}
			t.freq = append(t.freq, frequency{start, end, h, exact})
			maxSpan = max(maxSpan, end+t.times[len(t.times)-1].arrival-t.times[0].departure)
		}
		for _, t := range templates {
			sort.Slice(t.freq, func(i, j int) bool { return t.freq[i].start < t.freq[j].start })
			for i := 1; i < len(t.freq); i++ {
				if t.freq[i].start < t.freq[i-1].end {
					return fail(fmt.Errorf("overlapping frequencies"))
				}
			}
		}
	}
	calendars := map[string]calendar{}
	exceptions := map[string]map[string]int{}
	if _, ok := ts["calendar.txt"]; ok {
		rs, e := required(ts, "calendar.txt", "service_id", "start_date", "end_date", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday")
		if e != nil {
			return fail(e)
		}
		for _, r := range rs {
			if r["service_id"] == "" {
				return fail(fmt.Errorf("empty service"))
			}
			if _, ok := calendars[r["service_id"]]; ok {
				return fail(fmt.Errorf("duplicate calendar service"))
			}
			start, e := date(r["start_date"])
			if e != nil {
				return fail(e)
			}
			end, e := date(r["end_date"])
			if e != nil || end.Before(start) {
				return fail(fmt.Errorf("invalid calendar interval"))
			}
			c := calendar{start: start, end: end}
			for i, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
				if r[day] != "0" && r[day] != "1" {
					return fail(fmt.Errorf("invalid calendar weekday"))
				}
				c.week[i] = r[day] == "1"
			}
			calendars[r["service_id"]] = c
		}
	}
	if _, ok := ts["calendar_dates.txt"]; ok {
		rs, e := required(ts, "calendar_dates.txt", "service_id", "date", "exception_type")
		if e != nil {
			return fail(e)
		}
		for _, r := range rs {
			if r["service_id"] == "" {
				return fail(fmt.Errorf("empty exception service"))
			}
			if _, e = date(r["date"]); e != nil {
				return fail(e)
			}
			kind, e := number(r["exception_type"], 0)
			if e != nil || kind < 1 || kind > 2 {
				return fail(fmt.Errorf("invalid calendar exception"))
			}
			if exceptions[r["service_id"]] == nil {
				exceptions[r["service_id"]] = map[string]int{}
			}
			if exceptions[r["service_id"]][r["date"]] != 0 {
				return fail(fmt.Errorf("duplicate calendar exception"))
			}
			exceptions[r["service_id"]][r["date"]] = int(kind)
		}
	}
	for _, t := range templates {
		if _, ok := calendars[t.service]; !ok && len(exceptions[t.service]) == 0 {
			return fail(fmt.Errorf("trip has unknown service calendar"))
		}
	}
	active := func(service string, day time.Time) bool {
		if x := exceptions[service][day.Format("20060102")]; x != 0 {
			return x == 1
		}
		c, ok := calendars[service]
		return ok && !day.Before(c.start) && !day.After(c.end) && c.week[int(day.Weekday())]
	}
	presentTrips := map[string]bool{}
	presentRoutes := map[string]bool{}
	for dayOffset := -int(maxSpan / 86400); dayOffset <= int(opt.End/86400); dayOffset++ {
		day := base.AddDate(0, 0, dayOffset)
		offset := int64(dayOffset) * 86400
		for _, id := range order {
			t := templates[id]
			if !active(t.service, day) {
				continue
			}
			starts := []int64{t.times[0].departure}
			if len(t.freq) > 0 {
				starts = nil
				for _, f := range t.freq {
					for depart := f.start; depart < f.end; depart += f.headway {
						starts = append(starts, depart)
					}
				}
			}
			for _, start := range starts {
				shift := offset + start - t.times[0].departure
				if t.times[len(t.times)-1].arrival+shift < opt.Start || t.times[0].departure+shift > opt.End {
					continue
				}
				instance := stableID(t.id, day.Format("2006-01-02"), start)
				cs := []model.Connection{}
				for i := 1; i < len(t.times); i++ {
					a, b := t.times[i-1], t.times[i]
					dep, arr := a.departure+shift, b.arrival+shift
					if dep < opt.Start || arr > opt.End {
						continue
					}
					cs = append(cs, model.Connection{ID: stableID(instance, a.seq, b.seq), TripID: instance, Sequence: len(cs), From: a.stop, To: b.stop, Departure: dep, Arrival: arr, Pickup: a.pickup, Dropoff: b.dropoff})
				}
				if len(cs) == 0 {
					continue
				}
				if len(d.Connections)+len(cs) > opt.MaxConnections {
					return fail(fmt.Errorf("normalization connection budget exhausted; no partial snapshot returned"))
				}
				d.Connections = append(d.Connections, cs...)
				d.Trips = append(d.Trips, model.Trip{ID: instance, SourceID: t.id, RouteID: t.route, ServiceDate: day.Format("2006-01-02"), Accessible: t.accessible})
				presentTrips[t.id] = true
				presentRoutes[t.route] = true
			}
		}
	}
	if _, ok := ts["transfers.txt"]; ok {
		rs, e := required(ts, "transfers.txt", "from_stop_id", "to_stop_id", "transfer_type")
		if e != nil {
			return fail(e)
		}
		for i, r := range rs {
			kind, e := number(r["transfer_type"], 0)
			if e != nil || kind != 0 && kind != 2 && kind != 3 {
				return fail(fmt.Errorf("unsupported transfer type; timed/linked transfer semantics are not modeled"))
			}
			minimum, e := number(r["min_transfer_time"], 0)
			if e != nil || minimum < 0 || kind == 2 && r["min_transfer_time"] == "" || kind != 2 && minimum != 0 {
				return fail(fmt.Errorf("invalid minimum transfer time"))
			}
			for _, f := range []string{"from_stop_id", "to_stop_id"} {
				if _, ok := stopMap[r[f]]; !ok {
					return fail(fmt.Errorf("unknown transfer stop"))
				}
			}
			for _, side := range []string{"from", "to"} {
				tid, rid := r[side+"_trip_id"], r[side+"_route_id"]
				if tid != "" && (templates[tid] == nil || rid != "" && templates[tid].route != rid) {
					return fail(fmt.Errorf("invalid transfer trip selector"))
				}
				if rid != "" && !routeIDs[rid] {
					return fail(fmt.Errorf("invalid transfer route selector"))
				}
			}
			rule := model.TransferRule{ID: fmt.Sprintf("gtfs-transfer-%d", i+2), FromStop: r["from_stop_id"], ToStop: r["to_stop_id"], FromTrip: r["from_trip_id"], ToTrip: r["to_trip_id"], FromRoute: r["from_route_id"], ToRoute: r["to_route_id"], Type: int(kind), MinSeconds: minimum}
			if rule.FromTrip != "" && !presentTrips[rule.FromTrip] || rule.ToTrip != "" && !presentTrips[rule.ToTrip] || rule.FromRoute != "" && !presentRoutes[rule.FromRoute] || rule.ToRoute != "" && !presentRoutes[rule.ToRoute] {
				audit.InactiveTransferRules++
				continue
			}
			d.Transfers = append(d.Transfers, rule)
		}
	}
	if opt.TransferEdges == "minimum_time_proxy" {
		durations := map[[2]string]int64{}
		expand := func(id string) []string {
			if stopMap[id].LocationType == 0 {
				return []string{id}
			}
			ids := []string{}
			for _, s := range d.Stops {
				if s.LocationType == 0 && s.ParentStation == id {
					ids = append(ids, s.ID)
				}
			}
			return ids
		}
		for _, r := range d.Transfers {
			if r.Type != 2 {
				continue
			}
			for _, from := range expand(r.FromStop) {
				for _, to := range expand(r.ToStop) {
					if from == to {
						continue
					}
					if r.MinSeconds == 0 {
						return fail(fmt.Errorf("zero-time cross-stop proxy unsupported"))
					}
					k := [2]string{from, to}
					if old, ok := durations[k]; !ok || r.MinSeconds < old {
						durations[k] = r.MinSeconds
					}
				}
			}
		}
		keys := make([][2]string, 0, len(durations))
		for k := range durations {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		for _, k := range keys {
			d.Footpaths = append(d.Footpaths, model.Footpath{ID: stableID("minimum-time-proxy", k[0], k[1]), From: k[0], To: k[1], Duration: durations[k], Accessible: model.Unknown, Provenance: "inferred_gtfs_minimum_transfer_time_proxy"})
		}
		audit.Notes = append(audit.Notes, "Walking edges are explicitly inferred from minimum transfer times, which may contain buffers; they are not measured physical walks or OSM routes. No nearby-stop access/egress graph is inferred.")
	}
	if audit.HeadwayRows > 0 {
		audit.Notes = append(audit.Notes, "Headway service expanded at start+n*headway, end exclusive, phase zero; these are model events, not exact published departures. No stochastic reliability claim.")
	}
	audit.Notes = append(audit.Notes, "Snapshot includes active neighboring service dates needed by the event window; pre-window boardings and onboard-origin queries are outside the contract.", "Unsupported timed/linked transfers, block semantics, flex service and pathways cause rejection; fares/geometry/metadata tables are listed as ignored.")
	if e = d.Validate(); e != nil {
		return fail(e)
	}
	b, e := json.Marshal(d)
	if e != nil {
		return fail(e)
	}
	audit.NormalizedSHA256 = hash(append(b, '\n'))
	audit.InstanceCount = len(d.Trips)
	audit.ConnectionCount = len(d.Connections)
	audit.FootpathCount = len(d.Footpaths)
	return d, audit, nil
}

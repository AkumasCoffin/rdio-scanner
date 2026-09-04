// Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Id             any       `json:"id"`
	Audio          []byte    `json:"audio"`
	AudioName      any       `json:"audioName"`
	AudioType      any       `json:"audioType"`
	DateTime       time.Time `json:"dateTime"`
	Frequencies    any       `json:"frequencies"`
	Frequency      any       `json:"frequency"`
	Patches        any       `json:"patches"`
	Source         any       `json:"source"`
	Sources        any       `json:"sources"`
	System         uint      `json:"system"`
	Talkgroup      uint      `json:"talkgroup"`
	Delayed        bool      `json:"delayed,omitempty"`
	systemLabel    any
	talkgroupGroup any
	talkgroupLabel any
	talkgroupName  any
	talkgroupTag   any
	units          any
	apiKeyIdent    string
	// meta holds upload form fields the server does not itself recognise,
	// passed through to plugins as call.meta. This is how a plugin-implemented
	// server-to-server protocol carries hints on the upload without core
	// needing to know what they mean.
	meta map[string]string
	// pluginFields holds values contributed by plugins through
	// rdio.calls.extendField, merged into the wire payload by MarshalJSON.
	// Populated by Controller.ApplyPluginFields on the paths that serve a call
	// to a client; nil on every install with no field-extending plugin, which
	// is why this costs nothing when unused.
	pluginFields map[string]any
	// pluginBudget is the time allowance shared by every ingest point this call
	// passes through. It lives on the call because that is exactly its scope:
	// the five ingest points are separate dispatches, all charged to the same
	// upload, all on the single goroutine every other upload waits behind.
	// Created on first use, so an install with no plugins never allocates one.
	pluginBudget *pluginBudget
	// patchUpdate marks this value as a correction to a call already sent to
	// listeners rather than a call to play. It rides the same queue as a real
	// emit, so a correction can never overtake the call it corrects.
	patchUpdate bool
}

func NewCall() *Call {
	return &Call{
		Frequencies: []map[string]any{},
		Patches:     []uint{},
		Sources:     []map[string]any{},
	}
}

func (call *Call) IsValid() (ok bool, err error) {
	ok = true

	if len(call.Audio) <= 44 {
		ok = false
		err = errors.New("no audio")
	}

	if call.DateTime.Unix() == 0 {
		ok = false
		err = errors.New("no datetime")
	}

	if call.System < 1 {
		ok = false
		err = errors.New("no system")
	}

	if call.Talkgroup < 1 {
		ok = false
		err = errors.New("no talkgroup")
	}

	return ok, err
}

func (call *Call) MarshalJSON() ([]byte, error) {
	audio := fmt.Sprintf("%v", call.Audio)
	audio = strings.ReplaceAll(audio, " ", ",")

	out := map[string]any{
		"id": call.Id,
		"audio": map[string]any{
			"data": json.RawMessage(audio),
			"type": "Buffer",
		},
		"audioName":   call.AudioName,
		"audioType":   call.AudioType,
		"dateTime":    call.DateTime.UTC().Format(time.RFC3339),
		"frequencies": call.Frequencies,
		"frequency":   call.Frequency,
		"patches":     call.Patches,
		"source":      call.Source,
		"sources":     call.Sources,
		"system":      call.System,
		"talkgroup":   call.Talkgroup,
	}

	if call.Delayed {
		out["delayed"] = true
	}

	// Plugin-contributed fields go on last but never displace a core key, so a
	// plugin cannot reshape the call protocol out from under existing clients.
	for key, value := range call.pluginFields {
		if _, taken := out[key]; taken {
			continue
		}
		out[key] = value
	}

	return json.Marshal(out)
}

func (call *Call) ToJson() (string, error) {
	if b, err := json.Marshal(call); err == nil {
		return string(b), nil
	} else {
		return "", fmt.Errorf("call.tojson: %v", err)
	}
}

type Calls struct {
	mutex     sync.Mutex
	metaMutex sync.Mutex
	metaCache map[string]*callsSearchMeta

	// The unfiltered date bounds, remembered rather than asked for.
	//
	// `select dateTime from rdioScannerCalls order by dateTime asc limit 1`
	// looks like a one-row index peek and is not one on a table with
	// time-based retention: prune deletes the oldest rows, so the low end of
	// the time index fills with dead entries and the scan walks every one of
	// them before it reaches a live row. Measured at 21 seconds in production,
	// with the descending twin at 14, both re-run every 105 seconds to keep a
	// date picker's bounds warm.
	//
	// Neither needs a query in the steady state. The newest call is the one
	// just ingested, which WriteCall reports here; the oldest only moves when
	// prune deletes, which clears it alongside the cache. So the probes run
	// once at startup and then only after a prune.
	oldest time.Time
	newest time.Time
}

type callsSearchMeta struct {
	dateStart time.Time
	dateStop  time.Time
	count     uint
	expires   time.Time
}

// callsSearchMetaTTL — how long a cached search count/date-range is trusted.
//
// The count behind it is a count(*) over the whole calls table, which on a
// large database is a full scan. Refreshing that every few seconds costs more
// than the staleness it avoids: the number feeds a result count and a date
// picker, where being a minute out of date is unnoticeable.
const callsSearchMetaTTL = 2 * time.Minute

func NewCalls() *Calls {
	return &Calls{
		mutex:     sync.Mutex{},
		metaMutex: sync.Mutex{},
		metaCache: make(map[string]*callsSearchMeta),
	}
}

// InvalidateSearchMeta drops every cached range and count.
//
// Called on prune, where rows genuinely disappear and the earliest-call bound
// moves. It is deliberately NOT called on insert any more: a new call only
// ever extends the future edge of a range, every entry already expires within
// callsSearchMetaTTL, and wiping the cache per insert meant a busy install
// re-ran the bound probes and counts on essentially every search — the cache
// existed and did nothing. Bounds and counts may now lag reality by up to the
// TTL, which is the same freshness the stats dashboard already serves.
func (calls *Calls) InvalidateSearchMeta() {
	calls.metaMutex.Lock()
	calls.metaCache = make(map[string]*callsSearchMeta)
	// The oldest call is exactly what a prune moves, so it has to be found
	// again. The newest is untouched: prune deletes from the old end.
	calls.oldest = time.Time{}
	calls.metaMutex.Unlock()
}

// noteNewest records the most recent call seen, so the unfiltered upper bound
// never has to be queried for.
func (calls *Calls) noteNewest(at time.Time) {
	if at.IsZero() {
		return
	}

	calls.metaMutex.Lock()
	if at.After(calls.newest) {
		calls.newest = at
	}
	calls.metaMutex.Unlock()
}

// knownBounds returns the remembered unfiltered bounds. A zero time means not
// known yet, and only then is a probe worth running.
func (calls *Calls) knownBounds() (time.Time, time.Time) {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()

	return calls.oldest, calls.newest
}

func (calls *Calls) noteOldest(at time.Time) {
	calls.metaMutex.Lock()
	calls.oldest = at
	calls.metaMutex.Unlock()
}

func (calls *Calls) getSearchMeta(key string) (*callsSearchMeta, bool) {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()
	m, ok := calls.metaCache[key]
	if !ok || time.Now().After(m.expires) {
		return nil, false
	}
	return m, true
}

func (calls *Calls) putSearchMeta(key string, m *callsSearchMeta) {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()
	// Cap the cache: drop everything once it grows past the soft ceiling.
	// The cache is purely a TTL-bounded perf optimization, so clearing on
	// overflow only costs us one cold lookup per key.
	if len(calls.metaCache) > 256 {
		calls.metaCache = make(map[string]*callsSearchMeta)
	}
	calls.metaCache[key] = m
}

func (calls *Calls) CheckDuplicate(call *Call, msTimeFrame uint, db *Database) bool {
	_, found := calls.GetDuplicateId(call, msTimeFrame, db)

	return found
}

// GetDuplicateId is CheckDuplicate with the row it matched.
//
// The id is what lets a patched copy add the talkgroup it arrived on to the
// call already stored, instead of being dropped without trace.
func (calls *Calls) GetDuplicateId(call *Call, msTimeFrame uint, db *Database) (uint, bool) {
	var id sql.NullFloat64

	// Read-only — rely on the database driver's own concurrency guard.
	d := time.Duration(msTimeFrame) * time.Millisecond
	from := call.DateTime.Add(-d)
	to := call.DateTime.Add(d)

	df := db.DateTimeFormat
	query := fmt.Sprintf("select `id` from `rdioScannerCalls` where (`dateTime` between '%v' and '%v') and `system` = %v and `talkgroup` = %v order by `id` limit 1", from.Format(df), to.Format(df), call.System, call.Talkgroup)
	if err := db.QueryRow(query).Scan(&id); err != nil {
		return 0, false
	}

	if !id.Valid || id.Float64 <= 0 {
		return 0, false
	}

	return uint(id.Float64), true
}

// GetPatchDuplicate finds the stored call a patched copy duplicates, and the
// talkgroup it is currently filed under.
//
// Matched on the timestamp, not through the duplicate-detection window, and
// never wider than the patch was told to allow. With delay zero the timestamps
// must be equal, which is right when one recorder fans a transmission out to
// every member talkgroup: the copies are the same recording. Separate
// recorders on separate members keep their own clocks and can stamp the same
// transmission a second or two apart, which an equality test reads as two
// unrelated calls — hence the patch's delay, and hence it being per patch,
// since it is a property of that patch's recorders. The lookup covers every
// home the patch may have filed under: the first receiving talkgroup, and any
// higher-ranked one a promotion has since moved the call to.
//
// Where the window admits several candidates, the nearest in time wins. Any
// other rule would let a stale neighbour claim a copy that plainly belongs to
// the call beside it.
//
// The timestamps are bound parameters for the same reason the cursor's are: a
// literal built from DateTimeFormat does not match the text the driver wrote,
// and an equality test forgives nothing.
func (calls *Calls) GetPatchDuplicate(call *Call, homes []uint, delay uint, db *Database) (uint, uint, bool) {
	var (
		id        sql.NullFloat64
		talkgroup uint
	)

	if len(homes) == 0 {
		return 0, 0, false
	}

	at := call.DateTime.UTC()

	// The talkgroup this copy came in on is not one of the places its own
	// original could be sitting: a patched transmission reaches each member
	// once, so a second call on the same talkgroup is a second transmission,
	// or an ordinary duplicate, and either way not this one's other half.
	//
	// Leaving it in was harmless while the match was exact — two calls on one
	// talkgroup sharing a timestamp to the second really is one recording
	// twice — but a delay turns it into a window in which a talkgroup's normal
	// back-to-back traffic disappears into the call before it.
	others := make([]uint, 0, len(homes))

	for _, home := range homes {
		if home != call.Talkgroup {
			others = append(others, home)
		}
	}

	if len(others) == 0 {
		return 0, 0, false
	}

	homes = others

	marks := make([]string, len(homes))

	for i := range homes {
		marks[i] = "?"
	}

	if delay == 0 {
		// What patches did before the delay existed, and still the common
		// case: one equality test, one row.
		args := []any{at, call.System}

		for _, home := range homes {
			args = append(args, home)
		}

		err := db.QueryRow(
			fmt.Sprintf("select `id`, `talkgroup` from `rdioScannerCalls` where `dateTime` = ? and `system` = ? and `talkgroup` in (%s) order by `id` limit 1", strings.Join(marks, ", ")),
			args...,
		).Scan(&id, &talkgroup)
		if err != nil || !id.Valid || id.Float64 <= 0 {
			return 0, 0, false
		}

		return uint(id.Float64), talkgroup, true
	}

	window := time.Duration(delay) * time.Second
	args := []any{at.Add(-window), at.Add(window), call.System}

	for _, home := range homes {
		args = append(args, home)
	}

	// Distance from the copy's own timestamp is what decides this, and no
	// portable SQL expresses it across all three backends — so the window's
	// rows are read and compared here. The window bounds the count: a handful
	// at the delays this is meant for.
	rows, err := db.Query(
		fmt.Sprintf("select `id`, `talkgroup`, `dateTime` from `rdioScannerCalls` where (`dateTime` between ? and ?) and `system` = ? and `talkgroup` in (%s) order by `dateTime`, `id`", strings.Join(marks, ", ")),
		args...,
	)
	if err != nil {
		return 0, 0, false
	}

	defer rows.Close()

	var (
		bestId        uint
		bestTalkgroup uint
		bestGap       time.Duration
		found         bool
	)

	for rows.Next() {
		var (
			rowId        sql.NullFloat64
			rowTalkgroup uint
			rowDateTime  any
		)

		if err = rows.Scan(&rowId, &rowTalkgroup, &rowDateTime); err != nil {
			return 0, 0, false
		}

		if !rowId.Valid || rowId.Float64 <= 0 {
			continue
		}

		stored, perr := db.ParseDateTime(rowDateTime)
		if perr != nil {
			continue
		}

		gap := stored.UTC().Sub(at)
		if gap < 0 {
			gap = -gap
		}

		if !found || gap < bestGap {
			bestId = uint(rowId.Float64)
			bestTalkgroup = rowTalkgroup
			bestGap = gap
			found = true
		}
	}

	if err = rows.Err(); err != nil {
		return 0, 0, false
	}

	return bestId, bestTalkgroup, found
}

// PromoteCall refiles a stored call onto another talkgroup.
//
// This is the patch primary taking over: the call was filed under the
// secondary because nothing better had arrived, and now a copy really has
// arrived on the primary, so the call belongs there.
func (calls *Calls) PromoteCall(id uint, talkgroup uint, db *Database) error {
	calls.mutex.Lock()
	defer calls.mutex.Unlock()

	if _, err := db.Exec("update `rdioScannerCalls` set `talkgroup` = ? where `id` = ?", talkgroup, id); err != nil {
		return fmt.Errorf("calls.promotecall: %v", err)
	}

	return nil
}

// AddPatch records another talkgroup a stored call was received on.
//
// A patched transmission arrives once per talkgroup, and only the first copy
// becomes a row — the rest are dropped as duplicates. Without this the stored
// call would name only whichever copy happened to arrive first, and "received
// on Fireground and Tac" would read as "received on Fireground".
func (calls *Calls) AddPatch(id uint, talkgroup uint, db *Database) error {
	calls.mutex.Lock()
	defer calls.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("calls.addpatch: %v", err)
	}

	var current string

	if err := db.QueryRow("select `patches` from `rdioScannerCalls` where `id` = ?", id).Scan(&current); err != nil {
		return formatError(err)
	}

	patches := []uint{}

	if len(current) > 0 {
		if err := json.Unmarshal([]byte(current), &patches); err != nil {
			patches = []uint{}
		}
	}

	for _, existing := range patches {
		if existing == talkgroup {
			return nil
		}
	}

	patches = append(patches, talkgroup)

	b, err := json.Marshal(patches)
	if err != nil {
		return formatError(err)
	}

	if _, err = db.Exec("update `rdioScannerCalls` set `patches` = ? where `id` = ?", string(b), id); err != nil {
		return formatError(err)
	}

	return nil
}

// RefreshPatchState re-reads a stored call's talkgroup and received-talkgroup
// list into the in-memory copy.
//
// A patched transmission arrives once per talkgroup, so what a call was
// received on is not known when the first copy lands — only once its siblings
// have been collapsed into it. Anything holding a call between those two
// moments is holding a value whose patch list is already out of date, and
// sending it says the call was received on one talkgroup when the record says
// three.
func (calls *Calls) RefreshPatchState(call *Call, db *Database) error {
	id, ok := callIdAsUint(call.Id)
	if !ok {
		return nil
	}

	var (
		patches   sql.NullString
		talkgroup uint
	)

	if err := db.QueryRow(
		"select `talkgroup`, `patches` from `rdioScannerCalls` where `id` = ?", id,
	).Scan(&talkgroup, &patches); err != nil {
		return fmt.Errorf("calls.refreshpatchstate: %v", err)
	}

	call.Talkgroup = talkgroup
	call.Patches = []uint{}

	if patches.Valid && len(patches.String) > 0 {
		decoded := []uint{}

		if err := json.Unmarshal([]byte(patches.String), &decoded); err == nil {
			call.Patches = decoded
		}
	}

	return nil
}

func (calls *Calls) GetCall(id uint, db *Database) (*Call, error) {
	return calls.getCall(id, db, true)
}

// GetCallMeta is GetCall without the audio blob, for callers that only want a
// call's metadata.
//
// Worth a separate entry point because the blob is 50–200 KB and the cost is
// not just bandwidth: rdio.calls.get runs on the plugin's event loop, so a
// plugin asking for metadata was paying a blob transfer, on the loop, per
// call, for bytes it then discarded.
func (calls *Calls) GetCallMeta(id uint, db *Database) (*Call, error) {
	return calls.getCall(id, db, false)
}

func (calls *Calls) getCall(id uint, db *Database, withAudio bool) (*Call, error) {
	var (
		audioName   sql.NullString
		audioType   sql.NullString
		dateTime    any
		frequency   sql.NullFloat64
		source      sql.NullFloat64
		frequencies string
		patches     string
		sources     string
		t           time.Time
	)

	// No mutex here: database/sql is already goroutine-safe, and holding a
	// global lock around the read (which includes the audio blob transfer,
	// 50–200 KB) means click-to-play has to queue behind every in-flight
	// WriteCall from the ingest path. Removing the lock drops that queue.
	call := Call{Id: id}

	// A literal rather than the column when audio is not wanted, so the row
	// carries no blob and the scan target below stays the same shape.
	audioColumn := "`audio`"
	if !withAudio {
		audioColumn = "null"
	}

	query := fmt.Sprintf("select %s, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup` from `rdioScannerCalls` where `id` = %v", audioColumn, id)
	err := db.QueryRow(query).Scan(&call.Audio, &audioName, &audioType, &dateTime, &frequencies, &frequency, &patches, &source, &sources, &call.System, &call.Talkgroup)
	if err == sql.ErrNoRows {
		// Surface "not found" instead of returning a zombie empty Call.
		// Callers (CAL websocket handler, public API, admin retranscribe,
		// delayer refetch) all check err first, so the explicit signal lets
		// the websocket path log+drop the request and the public API return
		// a real 404 — previously the empty Call leaked downstream with
		// Audio=nil and the client silently bailed.
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("getcall: %v, %v", err, query)
	}

	if audioName.Valid {
		call.AudioName = audioName.String
	}

	if audioType.Valid {
		call.AudioType = audioType.String
	}

	if frequency.Valid && frequency.Float64 > 0 {
		call.Frequency = uint(frequency.Float64)
	}

	if t, err = db.ParseDateTime(dateTime); err == nil {
		call.DateTime = t
	} else {
		call.DateTime = time.Time{}
	}

	if len(frequencies) > 0 {
		if err = json.Unmarshal([]byte(frequencies), &call.Frequencies); err != nil {
			call.Frequencies = []any{}
		}
	}

	if len(patches) > 0 {
		if err = json.Unmarshal([]byte(patches), &call.Patches); err != nil {
			call.Patches = []any{}
		}
	}

	if source.Valid && source.Float64 > 0 {
		call.Source = uint(source.Float64)
	}

	if len(sources) > 0 {
		if err = json.Unmarshal([]byte(sources), &call.Sources); err != nil {
			call.Sources = []any{}
		}
	}

	return &call, nil
}

// GetIdByKey looks up the DB id of a call by (system, talkgroup, dateTime).
// Uses a ±500 ms window around dateTime — same approach as CheckDuplicate —
// so minor timezone/format differences between DB drivers don't cause misses.
// Returns 0 with no error when no matching call exists.
// GetIdByKey finds a call by the triple that identifies it from outside —
// system, talkgroup and when it happened.
//
// Bounded, unlike most reads here. This is what a plugin's calls.findId
// becomes, and it runs on the plugin's single event loop: an unbounded wait
// for a pooled connection does not stall one lookup, it stalls the whole
// plugin. Measured in production at 2m36s while the database was saturated,
// with the plugin's routes queued behind it the entire time. An error the
// plugin can see beats a stall nobody can.
func (calls *Calls) GetIdByKey(system uint, talkgroup uint, dateTime time.Time, db *Database) (uint, error) {
	var id sql.NullFloat64
	d := 500 * time.Millisecond
	from := dateTime.UTC().Add(-d).Format(db.DateTimeFormat)
	to := dateTime.UTC().Add(d).Format(db.DateTimeFormat)
	query := fmt.Sprintf("select `id` from `rdioScannerCalls` where `system` = %v and `talkgroup` = %v and (`dateTime` between '%v' and '%v') limit 1",
		system, talkgroup, from, to)

	ctx, cancel := context.WithTimeout(context.Background(), pluginLookupTimeout)
	defer cancel()

	// Safe to cancel on return: the row is scanned here and never handed on.
	if err := db.QueryRowContext(ctx, query).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("getidbykey: %v", err)
	}
	if id.Valid {
		return uint(id.Float64), nil
	}
	return 0, nil
}

func (calls *Calls) Prune(db *Database, pruneDays uint) error {
	date := time.Now().Add(-24 * time.Hour * time.Duration(pruneDays)).Format(db.DateTimeFormat)
	_, err := db.Exec("delete from `rdioScannerCalls` where `dateTime` < ?", date)

	return err
}

// callsSearchPlan is everything Calls.Search decides before it touches the
// database. Split out of Search itself so the where/order/cursor construction —
// the part with the injection surface and the backward-compatibility contract —
// can be asserted on directly by tests without a live query.
//
// Three where variants, because the three queries Search runs do not filter
// alike:
//   - probeWhere carries the content filters only. It feeds the two
//     `order by dateTime limit 1` bound probes, which report the extent of the
//     matching calls to the client's date picker; folding the picked date range
//     into them would collapse the bounds onto the current selection.
//   - where adds the date window, and is what count(*) measures.
//   - pageWhere adds the cursor predicate, and is what the page itself reads.
//
// The date window and the cursor are the only parts carrying bound parameters
// — whereArgs for `where`, pageArgs for `pageWhere`. They are bound rather than
// interpolated because a datetime literal has to match the exact text the
// driver wrote, and it does not: SQLite stores a bound time.Time in Go's own
// rendering, so a `dateTime = '...'` built from DateTimeFormat never matches a
// row and the cursor's tiebreak would silently skip every tied call. Handing
// the driver the time.Time makes both sides its problem, on all three backends.
type callsSearchPlan struct {
	probeWhere string
	where      string
	whereArgs  []any
	pageWhere  string
	pageArgs   []any
	order      string
	limit      uint
	offset     uint
	// withCount is false once the caller has opted into cursor paging. count(*)
	// over the calls table is the most expensive statement in this file — on a
	// large install it is a full scan — and it exists only to number pages that
	// a cursor client does not draw.
	withCount bool
	// probePairs, when set, is the exact (system, talkgroup) set probeWhere
	// matches, and licenses the fast form of the date-bound probes: one index
	// seek per pair instead of walking the time index past every
	// non-matching row. Nil whenever the filters cannot be reduced to a
	// finite pair set — a free-text term, a scoped client, the legacy scalar
	// filters — and the probes then run against probeWhere as always.
	probePairs []CallsSearchTalkgroup
}

// probePairLimit bounds the fast probe's union: past this many pairs the
// statement itself becomes the cost, and the plain probe walks less.
const probePairLimit = 200

// intersectScopes reduces several OR-of-(system, talkgroup) constraints ANDed
// together to the single pair set matching all of them.
func intersectScopes(sets []map[uint][]uint) map[uint][]uint {
	if len(sets) == 0 {
		return nil
	}

	result := map[uint][]uint{}

	for systemId, talkgroups := range sets[0] {
		result[systemId] = append([]uint{}, talkgroups...)
	}

	for _, set := range sets[1:] {
		next := map[uint][]uint{}

		for systemId, talkgroups := range result {
			allowed := map[uint]bool{}
			for _, talkgroup := range set[systemId] {
				allowed[talkgroup] = true
			}

			for _, talkgroup := range talkgroups {
				if allowed[talkgroup] {
					next[systemId] = append(next[systemId], talkgroup)
				}
			}
		}

		result = next
	}

	return result
}

// countKey is the search-meta cache key for this plan's count(*).
//
// The bound args are part of it: two searches differing only in their date
// window share a where clause now that the window is bound, and keying on the
// clause alone would serve one window's count for the other. Shared with
// WarmSearchMeta so the entry warmed at startup is the entry the first search
// looks for — they drifted apart the moment the key stopped being the raw
// where clause.
func (plan callsSearchPlan) countKey() string {
	return fmt.Sprintf("count:%v%v", plan.where, plan.whereArgs)
}

// buildCallsSearchPlan turns search options into SQL fragments.
//
// Every value that reaches a fragment is either a number parsed as a number, a
// time handed to the driver as a bound parameter, or a group/tag name that is
// used strictly as a map key and never interpolated — so nothing here widens
// the string-concatenated where clause into an injection path.
func buildCallsSearchPlan(searchOptions *CallsSearchOptions, client *Client, db *Database, searchExtensions []pluginResolvedSearch) callsSearchPlan {
	const (
		ascOrder  = "asc"
		descOrder = "desc"
	)

	var (
		limit uint
		order string
		where string = "true"

		// The structured half of what the string above accumulates: each
		// entry is one OR-of-pairs constraint, each systems filter one OR of
		// system ids, and probeExact goes false at any filter that cannot be
		// expressed as a finite pair set. See callsSearchPlan.probePairs.
		pairSets      []map[uint][]uint
		systemFilters [][]uint
		probeExact    = true
	)

	if client.Access != nil {
		switch v := client.Access.Systems.(type) {
		case []any:
			probeExact = false

			a := []string{}
			for _, scope := range v {
				var c string
				switch v := scope.(type) {
				case map[string]any:
					switch v["talkgroups"].(type) {
					case []any:
						b := strings.ReplaceAll(fmt.Sprintf("%v", v["talkgroups"]), " ", ", ")
						b = strings.ReplaceAll(b, "[", "(")
						b = strings.ReplaceAll(b, "]", ")")
						c = fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", v["id"], b)
					case string:
						if v["talkgroups"] == "*" {
							c = fmt.Sprintf("`system` = %v", v["id"])
						}
					}
				}
				if len(c) > 0 {
					a = append(a, c)
				}
			}
			where = fmt.Sprintf("(%s)", strings.Join(a, " or "))
		}
	}

	switch v := searchOptions.System.(type) {
	case uint:
		probeExact = false

		a := []string{
			fmt.Sprintf("`system` = %v", v),
		}
		switch v := searchOptions.Talkgroup.(type) {
		case uint:
			if searchOptions.searchPatchedTalkgroups {
				a = append(a, fmt.Sprintf("`talkgroup` = %v or patches = '%v' or patches like '[%v]' or patches like '[%v,%%' or patches like '%%,%v,%%' or patches like '%%,%v]'", v, v, v, v, v, v))
			} else {
				a = append(a, fmt.Sprintf("`talkgroup` = %v", v))
			}
		}
		where += fmt.Sprintf(" and (%s)", strings.Join(a, " and "))
	}

	// The plural filters below are all "match nothing" when the caller sent an
	// array that resolves to no ids. Silently dropping the clause would answer a
	// narrowed search with every call in the database, which reads as the filter
	// having worked — the same reasoning as the q= branch further down.
	switch v := searchOptions.Systems.(type) {
	case []uint:
		if len(v) > 0 {
			systemFilters = append(systemFilters, v)

			a := make([]string, 0, len(v))
			for _, id := range v {
				a = append(a, fmt.Sprintf("%v", id))
			}
			where += fmt.Sprintf(" and (`system` in (%s))", strings.Join(a, ", "))
		} else {
			where += " and 1 = 0"
		}
	}

	// Talkgroups arrive as (system, talkgroup) pairs rather than bare ids: a
	// talkgroup id is only unique inside its system, so a bare list would match
	// unrelated talkgroups that happen to share a number on another system.
	// Grouped back into one clause per system, which is the same shape the
	// access scoping above emits and the shape the (system, talkgroup) index
	// serves best.
	switch v := searchOptions.Talkgroups.(type) {
	case []CallsSearchTalkgroup:
		bySystem := map[uint][]uint{}
		systemIds := []uint{}
		for _, pair := range v {
			if _, seen := bySystem[pair.System]; !seen {
				systemIds = append(systemIds, pair.System)
			}
			bySystem[pair.System] = append(bySystem[pair.System], pair.Talkgroup)
		}
		pairSets = append(pairSets, bySystem)
		where += andScopeClause(bySystem, systemIds)
	}

	switch v := searchOptions.Group.(type) {
	case string:
		pairSets = append(pairSets, client.GroupsMap[v])

		a := []string{}
		for id, m := range client.GroupsMap[v] {
			b := strings.ReplaceAll(fmt.Sprintf("%v", m), " ", ", ")
			b = strings.ReplaceAll(b, "[", "(")
			b = strings.ReplaceAll(b, "]", ")")
			a = append(a, fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", id, b))
		}
		if len(a) > 0 {
			where += fmt.Sprintf(" and (%s)", strings.Join(a, " or "))
		}
	}

	switch v := searchOptions.Tag.(type) {
	case string:
		pairSets = append(pairSets, client.TagsMap[v])

		a := []string{}
		for id, m := range client.TagsMap[v] {
			b := strings.ReplaceAll(fmt.Sprintf("%v", m), " ", ", ")
			b = strings.ReplaceAll(b, "[", "(")
			b = strings.ReplaceAll(b, "]", ")")
			a = append(a, fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", id, b))
		}
		if len(a) > 0 {
			where += fmt.Sprintf(" and (%s)", strings.Join(a, " or "))
		}
	}

	// Group and tag names never reach the SQL: they are map keys into the
	// client's own scoped view, and only the numeric ids that come back out are
	// interpolated. A name the client cannot see resolves to nothing, so the
	// plural forms cannot be used to widen a scoped client's reach either.
	switch v := searchOptions.Groups.(type) {
	case []string:
		bySystem, systemIds := mergeScopes(v, client.GroupsMap)
		pairSets = append(pairSets, bySystem)
		where += andScopeClause(bySystem, systemIds)
	}

	switch v := searchOptions.Tags.(type) {
	case []string:
		bySystem, systemIds := mergeScopes(v, client.TagsMap)
		pairSets = append(pairSets, bySystem)
		where += andScopeClause(bySystem, systemIds)
	}

	if q, ok := searchOptions.Q.(string); ok && q != "" {
		probeExact = false

		esc := strings.ReplaceAll(q, "'", "''")
		op := "like"
		if db.Config.DbType == DbTypePostgres {
			op = "ilike"
		}

		// Free-text search runs entirely over columns plugins registered. The
		// server itself stores no searchable text on a call, so with no such
		// plugin installed a q= search correctly matches nothing rather than
		// silently ignoring the filter.
		predicates := []string{}

		for _, extension := range searchExtensions {
			predicates = append(predicates, fmt.Sprintf(
				"exists (select 1 from `%s` where `%s`.`%s` = `rdioScannerCalls`.`id` and `%s`.`%s` %s '%%%s%%')",
				extension.table,
				extension.table, extension.key,
				extension.table, extension.text,
				op, esc,
			))
		}

		if len(predicates) > 0 {
			where += fmt.Sprintf(" and (%s)", strings.Join(predicates, " or "))
		} else {
			// Nothing searchable is registered. Matching nothing is the honest
			// answer — quietly dropping the filter would return every call and
			// look like the search had worked.
			where += " and 1 = 0"
		}
	}

	// Calls that carry a plugin-contributed field, or that do not.
	//
	// Same exists() shape as the free-text filter above, minus the comparison:
	// the question is whether a row is there and holds something, not what it
	// holds. Empty strings count as absent — text that came back empty is
	// still stored as a row, and treating that as "has one" would hide exactly
	// the calls this filter exists to find.
	//
	// The field is matched by the name the plugin registered, so core carries
	// no idea of what the text is. A name nothing registers matches nothing,
	// which is also what a filter left set behind an uninstalled plugin does.
	hasField, _ := searchOptions.HasField.(string)
	lacksField, _ := searchOptions.LacksField.(string)

	if field := strings.TrimSpace(hasField); field != "" {
		probeExact = false
		where += fmt.Sprintf(" and %s", fieldPresencePredicate(searchExtensions, field, true))
	}

	if field := strings.TrimSpace(lacksField); field != "" {
		probeExact = false
		where += fmt.Sprintf(" and %s", fieldPresencePredicate(searchExtensions, field, false))
	}

	// Everything above narrows *which* calls exist for this search, so it is
	// what the date-bound probes measure. Everything below picks a window and a
	// page inside that set.
	plan := callsSearchPlan{probeWhere: where, withCount: true}

	// The fast probe form needs the filters to reduce to a finite pair set:
	// at least one pair constraint, everything else expressible against it.
	if probeExact && len(pairSets) > 0 {
		pairs := intersectScopes(pairSets)

		for _, systems := range systemFilters {
			allowed := map[uint]bool{}
			for _, id := range systems {
				allowed[id] = true
			}
			for systemId := range pairs {
				if !allowed[systemId] {
					delete(pairs, systemId)
				}
			}
		}

		flat := []CallsSearchTalkgroup{}
		systemIds := make([]uint, 0, len(pairs))
		for systemId := range pairs {
			systemIds = append(systemIds, systemId)
		}
		sort.Slice(systemIds, func(i, j int) bool { return systemIds[i] < systemIds[j] })
		for _, systemId := range systemIds {
			talkgroups := append([]uint{}, pairs[systemId]...)
			sort.Slice(talkgroups, func(i, j int) bool { return talkgroups[i] < talkgroups[j] })
			for _, talkgroup := range talkgroups {
				flat = append(flat, CallsSearchTalkgroup{System: systemId, Talkgroup: talkgroup})
			}
		}

		if len(flat) > 0 && len(flat) <= probePairLimit {
			plan.probePairs = flat
		}
	}

	switch v := searchOptions.Sort.(type) {
	case float64:
		if v < 0 {
			order = descOrder
		} else {
			order = ascOrder
		}
	default:
		order = ascOrder
	}

	// The id tiebreak is what makes a cursor stable: calls routinely share a
	// dateTime to the stored precision, and `order by dateTime` alone leaves the
	// backend free to return those rows in a different order on the next page —
	// which is exactly how a cursor skips or repeats rows.
	plan.order = fmt.Sprintf("`dateTime` %v, `id` %v", order, order)

	switch v := searchOptions.Date.(type) {
	case time.Time:
		var (
			df    string = db.DateTimeFormat
			start time.Time
			stop  time.Time
		)

		if order == ascOrder {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC)
			stop = start.Add(time.Hour*24 - time.Millisecond)

		} else {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC).Add(time.Hour*-24 + time.Millisecond)
			stop = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC)
		}

		where += fmt.Sprintf(" and (`dateTime` between '%v' and '%v')", start.Format(df), stop.Format(df))
	}

	// dateStart/dateStop is the plain inclusive window `date` never was: give it
	// both ends and it is a span of days, both ends on one day and it is a slice
	// of that day, one end and it is open-ended. `date` keeps its own ±24h
	// anchored-on-sort behaviour above, because the Android app and the plugins
	// depend on it.
	dateStart, hasDateStart := searchOptions.DateStart.(time.Time)
	dateStop, hasDateStop := searchOptions.DateStop.(time.Time)

	// Always in UTC: a bound time carries its zone, and the same instant in
	// another zone is a different value to an equality test on SQLite.
	switch {
	case hasDateStart && hasDateStop:
		where += " and (`dateTime` between ? and ?)"
		plan.whereArgs = append(plan.whereArgs, dateStart.UTC(), dateStop.UTC())
	case hasDateStart:
		where += " and (`dateTime` >= ?)"
		plan.whereArgs = append(plan.whereArgs, dateStart.UTC())
	case hasDateStop:
		where += " and (`dateTime` <= ?)"
		plan.whereArgs = append(plan.whereArgs, dateStop.UTC())
	}

	plan.where = where
	// Copied rather than aliased: the cursor appends to pageArgs, and a shared
	// backing array would let that write land in the count query's args.
	plan.pageArgs = append([]any{}, plan.whereArgs...)

	switch v := searchOptions.Limit.(type) {
	case uint:
		limit = uint(math.Min(float64(500), float64(v)))
	default:
		limit = 200
	}
	plan.limit = limit

	switch v := searchOptions.Offset.(type) {
	case uint:
		plan.offset = v
	}

	// A caller that says it pages by cursor gets no count(*), whether or not
	// this particular request carries a cursor — the first page of a cursor walk
	// has none, and that page is exactly where the scan used to be paid. Callers
	// that still send offset keep their count, so nothing that draws numbered
	// pages regresses.
	if v, ok := searchOptions.Cursor.(bool); ok && v {
		plan.withCount = false
	}

	switch v := searchOptions.After.(type) {
	case *CallsSearchCursor:
		plan.withCount = false

		// Written out rather than as a row-value comparison — (dateTime, id) <
		// (:d, :id) — because row-value support differs across the three
		// backends this server runs on.
		if order == descOrder {
			where += " and (`dateTime` < ? or (`dateTime` = ? and `id` < ?))"
		} else {
			where += " and (`dateTime` > ? or (`dateTime` = ? and `id` > ?))"
		}
		plan.pageArgs = append(plan.pageArgs, v.DateTime.UTC(), v.DateTime.UTC(), v.Id)

		// The cursor *is* the position. Applying offset on top of it would skip
		// a page's worth of rows on every request after the first.
		plan.offset = 0
	}

	plan.pageWhere = where

	return plan
}

// mergeScopes folds the talkgroups of several named groups (or tags) into one
// system -> talkgroups map, plus the system ids in a stable order. Sorted
// rather than map order because this string ends up as the search-meta cache
// key: a where clause that shuffles between identical searches is a cache that
// never hits.
func mergeScopes(names []string, scopes map[string]map[uint][]uint) (map[uint][]uint, []uint) {
	bySystem := map[uint][]uint{}
	seen := map[uint]map[uint]bool{}
	systemIds := []uint{}

	for _, name := range names {
		for systemId, talkgroups := range scopes[name] {
			if seen[systemId] == nil {
				seen[systemId] = map[uint]bool{}
				systemIds = append(systemIds, systemId)
			}
			for _, talkgroup := range talkgroups {
				// Two named groups can share a talkgroup; listing it twice is
				// harmless but noisy in the cache key.
				if seen[systemId][talkgroup] {
					continue
				}
				seen[systemId][talkgroup] = true
				bySystem[systemId] = append(bySystem[systemId], talkgroup)
			}
		}
	}

	sort.Slice(systemIds, func(i, j int) bool { return systemIds[i] < systemIds[j] })
	for _, systemId := range systemIds {
		sort.Slice(bySystem[systemId], func(i, j int) bool { return bySystem[systemId][i] < bySystem[systemId][j] })
	}

	return bySystem, systemIds
}

// andScopeClause renders a system -> talkgroups map as the same
// `(system = X and talkgroup in (...)) or ...` shape the access scoping uses,
// prefixed with " and " ready to append. An empty map matches nothing, which is
// the honest answer for a filter the caller did set.
func andScopeClause(bySystem map[uint][]uint, systemIds []uint) string {
	if len(systemIds) == 0 {
		return " and 1 = 0"
	}

	a := make([]string, 0, len(systemIds))
	for _, systemId := range systemIds {
		b := make([]string, 0, len(bySystem[systemId]))
		for _, talkgroup := range bySystem[systemId] {
			b = append(b, fmt.Sprintf("%v", talkgroup))
		}
		a = append(a, fmt.Sprintf("(`system` = %v and `talkgroup` in (%s))", systemId, strings.Join(b, ", ")))
	}

	return fmt.Sprintf(" and (%s)", strings.Join(a, " or "))
}

func (calls *Calls) Search(searchOptions *CallsSearchOptions, client *Client) (*CallsSearchResults, error) {
	var (
		dateTime any
		err      error
		id       sql.NullFloat64
		query    string
		rows     *sql.Rows
		t        time.Time
	)

	// Read-only aggregate; no need to serialize behind ingests.
	db := client.Controller.Database

	formatError := func(err error) error {
		return fmt.Errorf("calls.search: %v", err)
	}

	searchResults := &CallsSearchResults{
		Options: searchOptions,
		Results: []CallsSearchResult{},
	}

	// Plugin-contributed searchable columns, resolved once and reused for both
	// the free-text filter and the result lookup further down.
	var searchExtensions []pluginResolvedSearch
	if client != nil && client.Controller != nil {
		searchExtensions = client.Controller.PluginSearchExtensions()
	}

	plan := buildCallsSearchPlan(searchOptions, client, db, searchExtensions)

	rangeKey := "range:" + plan.probeWhere
	if cached, ok := calls.getSearchMeta(rangeKey); ok {
		searchResults.DateStart = cached.dateStart
		searchResults.DateStop = cached.dateStop
	} else {
		// One statement per bound. With an exact pair set, each bound is the
		// min (or max) over per-pair index seeks on (system, talkgroup,
		// dateTime) — milliseconds however large the table — where the plain
		// form walks the time index discarding every non-matching row.
		probe := func(direction string) (any, error) {
			var value any

			if plan.probePairs != nil {
				arm := fmt.Sprintf("select %s(`dateTime`) as dt from `rdioScannerCalls` where `system` = ? and `talkgroup` = ?", direction)
				arms := make([]string, len(plan.probePairs))
				args := make([]any, 0, 2*len(plan.probePairs))

				for i, pair := range plan.probePairs {
					arms[i] = arm
					args = append(args, pair.System, pair.Talkgroup)
				}

				query := fmt.Sprintf("select %s(dt) from (%s) as bounds", direction, strings.Join(arms, " union all "))
				err := db.QueryRow(query, args...).Scan(&value)

				return value, err
			}

			order := "asc"
			if direction == "max" {
				order = "desc"
			}

			query := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %v order by `dateTime` %s limit 1", plan.probeWhere, order)
			err := db.QueryRow(query).Scan(&value)

			return value, err
		}

		if dateTime, err = probe("min"); err != nil && err != sql.ErrNoRows {
			return nil, formatError(err)
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResults.DateStart = t
		}

		if dateTime, err = probe("max"); err != nil && err != sql.ErrNoRows {
			return nil, formatError(err)
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResults.DateStop = t
		} else {
			searchResults.DateStop = time.Now()
		}

		calls.putSearchMeta(rangeKey, &callsSearchMeta{
			dateStart: searchResults.DateStart,
			dateStop:  searchResults.DateStop,
			expires:   time.Now().Add(callsSearchMetaTTL),
		})
	}

	if plan.withCount {
		countKey := plan.countKey()
		if cached, ok := calls.getSearchMeta(countKey); ok {
			searchResults.Count = cached.count
		} else {
			// An unfiltered search counts the whole table, which is the one
			// count the planner's estimate can stand in for.
			if estimate, ok := db.ApproxCallCount(); ok && plan.where == "true" {
				searchResults.Count = estimate
			} else {
				query = fmt.Sprintf("select count(*) from `rdioScannerCalls` where %v", plan.where)
				if err = db.QueryRow(query, plan.whereArgs...).Scan(&searchResults.Count); err != nil && err != sql.ErrNoRows {
					return nil, formatError(fmt.Errorf("%v, %v", err, query))
				}
			}
			calls.putSearchMeta(countKey, &callsSearchMeta{
				count:   searchResults.Count,
				expires: time.Now().Add(callsSearchMetaTTL),
			})
		}
	}

	query = fmt.Sprintf("select `id`, `dateTime`, `system`, `talkgroup`, `patches` from `rdioScannerCalls` where %v order by %v limit %v offset %v", plan.pageWhere, plan.order, plan.limit, plan.offset)
	if rows, err = db.Query(query, plan.pageArgs...); err != nil {
		return nil, formatError(fmt.Errorf("%v, %v", err, query))
	}
	defer rows.Close()

	for rows.Next() {
		var patches sql.NullString

		searchResult := CallsSearchResult{}
		if err = rows.Scan(&id, &dateTime, &searchResult.System, &searchResult.Talkgroup, &patches); err != nil {
			break
		}

		if patches.Valid && len(patches.String) > 2 {
			// Best effort: a row whose patches column will not parse is still
			// a perfectly good search result.
			_ = json.Unmarshal([]byte(patches.String), &searchResult.Patches)
		}

		if id.Valid && id.Float64 > 0 {
			searchResult.Id = uint(id.Float64)
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResult.DateTime = t

		} else {
			continue
		}

		searchResults.Results = append(searchResults.Results, searchResult)
	}

	if err != nil {
		return nil, formatError(err)
	}

	// Fill in plugin-contributed result fields. Batched: one extra query per
	// extension for the whole page, rather than one per row — a page can be 200
	// calls, and per-row lookups would turn one search into 200 round trips.
	if len(searchExtensions) > 0 && len(searchResults.Results) > 0 {
		calls.applyPluginSearchFields(db, searchExtensions, searchResults.Results)
	}

	return searchResults, err
}

// applyPluginSearchFields loads each extension's values for a page of results.
// Failures are logged nowhere and simply skipped: a plugin's own schema problem
// must not break call search for everyone.
func (calls *Calls) applyPluginSearchFields(db *Database, extensions []pluginResolvedSearch, results []CallsSearchResult) {
	ids := make([]string, 0, len(results))
	byId := map[uint]*CallsSearchResult{}

	for i := range results {
		ids = append(ids, fmt.Sprintf("%d", results[i].Id))
		byId[results[i].Id] = &results[i]
	}

	idList := strings.Join(ids, ", ")

	for _, extension := range extensions {
		query := fmt.Sprintf(
			"select `%s`, `%s` from `%s` where `%s` in (%s)",
			extension.key, extension.text, extension.table, extension.key, idList,
		)

		rows, err := db.Query(query)
		if err != nil {
			continue
		}

		for rows.Next() {
			var (
				callId uint
				value  sql.NullString
			)

			if err := rows.Scan(&callId, &value); err != nil {
				break
			}

			if !value.Valid || value.String == "" {
				continue
			}

			if result, ok := byId[callId]; ok {
				if result.pluginFields == nil {
					result.pluginFields = map[string]any{}
				}
				result.pluginFields[extension.resultField] = value.String
			}
		}

		rows.Close()
	}
}

// UpdateAudio replaces a stored call's audio and the two fields describing it.
//
// Deliberately narrow. This is the write path for a plugin that reprocesses a
// call after the fact — noise reduction, a different encoding — and nothing
// else about the row is reachable through it.
func (calls *Calls) UpdateAudio(call *Call, db *Database) error {
	calls.mutex.Lock()
	defer calls.mutex.Unlock()

	_, err := db.Exec(
		"update `rdioScannerCalls` set `audio` = ?, `audioName` = ?, `audioType` = ? where `id` = ?",
		call.Audio, call.AudioName, call.AudioType, call.Id,
	)
	if err != nil {
		return fmt.Errorf("call.updateAudio: %s", err.Error())
	}

	return nil
}

func (calls *Calls) WriteCall(call *Call, db *Database) (uint, error) {
	var (
		b           []byte
		err         error
		frequencies string
		id          int64
		patches     string
		res         sql.Result
		sources     string
	)

	calls.mutex.Lock()
	defer calls.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("call.write: %s", err.Error())
	}

	switch v := call.Frequencies.(type) {
	case []map[string]any:
		if b, err = json.Marshal(v); err == nil {
			frequencies = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	switch v := call.Patches.(type) {
	case []uint:
		if b, err = json.Marshal(v); err == nil {
			patches = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	switch v := call.Sources.(type) {
	case []map[string]any:
		if b, err = json.Marshal(v); err == nil {
			sources = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	if db.Config.DbType == DbTypePostgres {
		err = db.QueryRow("insert into `rdioScannerCalls` (`audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) returning `id`",
			call.Audio, call.AudioName, call.AudioType, call.DateTime, frequencies, call.Frequency, patches, call.Source, sources, call.System, call.Talkgroup).Scan(&id)
		if err != nil {
			return 0, formatError(err)
		}

		// The unfiltered upper bound, without anyone having to go and look for
		// it. See the note on Calls.newest.
		calls.noteNewest(call.DateTime)

		return uint(id), nil
	}

	if res, err = db.Exec("insert into `rdioScannerCalls` (`id`, `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", call.Id, call.Audio, call.AudioName, call.AudioType, call.DateTime, frequencies, call.Frequency, patches, call.Source, sources, call.System, call.Talkgroup); err != nil {
		return 0, formatError(err)
	}

	if id, err = res.LastInsertId(); err == nil {
		calls.noteNewest(call.DateTime)

		return uint(id), nil
	} else {
		return 0, formatError(err)
	}
}

// WarmSearchMeta populates the unscoped metadata cache with dateStart,
// dateStop, and count(*) so the first search request doesn't pay the
// cold-start penalty. Safe to call in a goroutine.
func (calls *Calls) WarmSearchMeta(db *Database) {
	const where = "true"
	var (
		dateTime any
		t        time.Time
	)

	start, stop := calls.knownBounds()

	// Only when it is genuinely unknown — at startup, and after a prune has
	// moved it. Every other run of this reuses the answer.
	if start.IsZero() {
		startQuery := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %s order by `dateTime` asc limit 1", where)
		if err := db.QueryRow(startQuery).Scan(&dateTime); err == nil {
			if t, err = db.ParseDateTime(dateTime); err == nil {
				start = t
				calls.noteOldest(t)
			}
		}
	}

	// The upper bound comes from ingest, which knows it without asking. Only a
	// server that has not stored a call since it started has to look.
	if stop.IsZero() {
		stopQuery := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %s order by `dateTime` desc limit 1", where)
		if err := db.QueryRow(stopQuery).Scan(&dateTime); err == nil {
			if t, err = db.ParseDateTime(dateTime); err == nil {
				stop = t
				calls.noteNewest(t)
			}
		}
	}

	if stop.IsZero() {
		stop = time.Now()
	}

	calls.putSearchMeta("range:"+where, &callsSearchMeta{
		dateStart: start,
		dateStop:  stop,
		expires:   time.Now().Add(callsSearchMetaTTL),
	})

	var (
		count uint
		ok    bool
	)

	if count, ok = db.ApproxCallCount(); !ok {
		countQuery := fmt.Sprintf("select count(*) from `rdioScannerCalls` where %s", where)
		if err := db.QueryRow(countQuery).Scan(&count); err != nil {
			return
		}
	}

	{
		// Through the plan's own key builder, so an unfiltered search finds
		// this entry instead of paying for the scan it was warmed to avoid.
		calls.putSearchMeta(callsSearchPlan{where: where}.countKey(), &callsSearchMeta{
			count:   count,
			expires: time.Now().Add(callsSearchMetaTTL),
		})
	}
}

// CallsSearchTalkgroup is one (system, talkgroup) pair of the `talkgroups`
// filter. A pair rather than a bare id because a talkgroup id is only unique
// within its system.
type CallsSearchTalkgroup struct {
	System    uint `json:"system"`
	Talkgroup uint `json:"talkgroup"`
}

// CallsSearchCursor is the position of the last row a client already has. Both
// halves are needed: dateTime alone does not identify a row, since calls share
// timestamps routinely.
type CallsSearchCursor struct {
	DateTime time.Time `json:"dateTime"`
	Id       uint      `json:"id"`
}

// CallsSearchOptions is the wire shape of a call search.
//
// Everything is `any` because absent and zero must stay distinguishable —
// system 0 is not a system, but limit 0 and sort 0 are real values — and
// because the singular filters predate the plural ones and must keep their
// exact behaviour for the Android app and for plugins.
// fieldPresencePredicate builds "this call has that plugin field" — or its
// negation — over every extension registered under that name.
//
// More than one plugin may register the same result field; a call having it
// from any of them counts, so "has" is an OR and "lacks" is the negation of the
// same OR rather than an AND of nots, which would mean something subtly
// different once two plugins were installed.
func fieldPresencePredicate(searchExtensions []pluginResolvedSearch, field string, present bool) string {
	predicates := []string{}

	for _, extension := range searchExtensions {
		if extension.resultField != field {
			continue
		}

		predicates = append(predicates, fmt.Sprintf(
			"exists (select 1 from `%s` where `%s`.`%s` = `rdioScannerCalls`.`id` and `%s`.`%s` is not null and `%s`.`%s` <> '')",
			extension.table,
			extension.table, extension.key,
			extension.table, extension.text,
			extension.table, extension.text,
		))
	}

	// Nothing registers that field, so no call can carry it. Saying so beats
	// dropping the filter, which would return everything and look like it had
	// worked.
	if len(predicates) == 0 {
		if present {
			return "1 = 0"
		}
		return "1 = 1"
	}

	joined := strings.Join(predicates, " or ")

	if present {
		return fmt.Sprintf("(%s)", joined)
	}

	return fmt.Sprintf("not (%s)", joined)
}

type CallsSearchOptions struct {
	After      any `json:"after,omitempty"`
	Cursor     any `json:"cursor,omitempty"`
	Date       any `json:"date,omitempty"`
	DateStart  any `json:"dateStart,omitempty"`
	DateStop   any `json:"dateStop,omitempty"`
	Group      any `json:"group,omitempty"`
	Groups     any `json:"groups,omitempty"`
	Limit      any `json:"limit,omitempty"`
	Offset     any `json:"offset,omitempty"`
	Q          any `json:"q,omitempty"`
	Sort       any `json:"sort,omitempty"`
	System     any `json:"system,omitempty"`
	Systems    any `json:"systems,omitempty"`
	Tag        any `json:"tag,omitempty"`
	Tags       any `json:"tags,omitempty"`
	Talkgroup  any `json:"talkgroup,omitempty"`
	Talkgroups any `json:"talkgroups,omitempty"`

	// HasField / LacksField narrow to calls that carry a plugin-contributed
	// text field, or that do not. The value is the field's name as the plugin
	// registered it through rdio.search.extend — "transcript" for the
	// transcripts plugin — and core attaches no meaning to it beyond matching
	// it against what is registered.
	//
	// Naming the field rather than the concept is what keeps this out of the
	// business of knowing what a transcript is. An unknown name matches
	// nothing, which is also what happens once the plugin behind it is
	// uninstalled.
	HasField   any `json:"hasField,omitempty"`
	LacksField any `json:"lacksField,omitempty"`

	searchPatchedTalkgroups bool
}

// jsonStrings reads a JSON (or goja-exported) array of strings. The second
// return distinguishes "the caller sent no such key" from "the caller sent an
// array that holds nothing usable" — the two mean opposite things to a filter.
func jsonStrings(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []any:
		out := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out, true
	case []string:
		return v, true
	}

	return nil, false
}

func (searchOptions *CallsSearchOptions) fromMap(m map[string]any) error {
	switch v := m["date"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			searchOptions.Date = t
		}
	}

	// dateStart/dateStop are only ever set together with what they parsed to: a
	// string that is not RFC3339 leaves the field unset, so a malformed bound
	// widens the search rather than pinning it to the zero time (year 1) and
	// returning nothing.
	switch v := m["dateStart"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			searchOptions.DateStart = t
		}
	}

	switch v := m["dateStop"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			searchOptions.DateStop = t
		}
	}

	switch v := m["group"].(type) {
	case string:
		searchOptions.Group = v
	}

	if v, ok := jsonStrings(m["groups"]); ok {
		searchOptions.Groups = v
	}

	if v, ok := jsonStrings(m["tags"]); ok {
		searchOptions.Tags = v
	}

	// A present-but-unusable array stays present (as an empty slice) rather than
	// falling back to unset: the caller asked to narrow the search, and answering
	// with every call would look like the filter had worked.
	switch v := m["systems"].(type) {
	case []any:
		systems := []uint{}
		for _, raw := range v {
			if id, ok := jsonUint(raw); ok {
				systems = append(systems, id)
			}
		}
		searchOptions.Systems = systems
	}

	switch v := m["talkgroups"].(type) {
	case []any:
		talkgroups := []CallsSearchTalkgroup{}
		for _, raw := range v {
			pair, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			system, hasSystem := jsonUintFrom(pair, "system")
			talkgroup, hasTalkgroup := jsonUintFrom(pair, "talkgroup")
			if !hasSystem || !hasTalkgroup {
				continue
			}
			talkgroups = append(talkgroups, CallsSearchTalkgroup{System: system, Talkgroup: talkgroup})
		}
		searchOptions.Talkgroups = talkgroups
	}

	// A cursor is only honoured whole. Half of one — a dateTime with no id, or
	// an unparsable dateTime — would page from an ambiguous position, which is
	// how a walk silently skips or repeats rows.
	switch v := m["after"].(type) {
	case map[string]any:
		if raw, ok := v["dateTime"].(string); ok {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				if id, ok := jsonUintFrom(v, "id"); ok {
					searchOptions.After = &CallsSearchCursor{DateTime: t, Id: id}
				}
			}
		}
	}

	switch v := m["cursor"].(type) {
	case bool:
		searchOptions.Cursor = v
	}

	if v, ok := jsonUint(m["limit"]); ok {
		searchOptions.Limit = v
	}

	if v, ok := jsonUint(m["offset"]); ok {
		searchOptions.Offset = v
	}

	if v, ok := jsonFloat(m["sort"]); ok {
		searchOptions.Sort = v
	}

	if v, ok := jsonUint(m["system"]); ok {
		searchOptions.System = v
	}

	switch v := m["tag"].(type) {
	case string:
		searchOptions.Tag = v
	}

	if v, ok := jsonUint(m["talkgroup"]); ok {
		searchOptions.Talkgroup = v
	}

	switch v := m["q"].(type) {
	case string:
		s := strings.TrimSpace(v)
		if s != "" {
			searchOptions.Q = s
		}
	}

	if v, ok := m["hasField"].(string); ok {
		if s := strings.TrimSpace(v); s != "" {
			searchOptions.HasField = s
		}
	}

	if v, ok := m["lacksField"].(string); ok {
		if s := strings.TrimSpace(v); s != "" {
			searchOptions.LacksField = s
		}
	}

	return nil
}

type CallsSearchResult struct {
	Id        uint      `json:"id"`
	DateTime  time.Time `json:"dateTime"`
	System    uint      `json:"system"`
	Talkgroup uint      `json:"talkgroup"`
	// Patches — the other talkgroups this call was received on. Carried so
	// the search list can mark a patched call without fetching each call.
	Patches []uint `json:"patches,omitempty"`
	// pluginFields holds values contributed by plugins through
	// rdio.search.extend, merged into the wire payload by MarshalJSON. Nil on
	// any install with no such plugin, which is why this costs nothing unused.
	pluginFields map[string]any
}

// MarshalJSON emits the core fields plus anything plugins contributed. Plugin
// values never displace a core key, so a plugin cannot reshape the search
// protocol out from under an existing client.
func (result CallsSearchResult) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"id":        result.Id,
		"dateTime":  result.DateTime,
		"system":    result.System,
		"talkgroup": result.Talkgroup,
	}

	if len(result.Patches) > 0 {
		out["patches"] = result.Patches
	}

	for key, value := range result.pluginFields {
		if _, taken := out[key]; taken {
			continue
		}
		out[key] = value
	}

	return json.Marshal(out)
}

type CallsSearchResults struct {
	Count     uint                `json:"count"`
	DateStart time.Time           `json:"dateStart"`
	DateStop  time.Time           `json:"dateStop"`
	Options   *CallsSearchOptions `json:"options"`
	Results   []CallsSearchResult `json:"results"`
}

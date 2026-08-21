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
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// statementTimeout bounds a single write so a wedged database can't stall a
// caller forever. Deliberately generous: the goal is to turn "blocks until the
// process is restarted" into "eventually returns an error", not to police slow
// queries. Legitimate heavy writes (call pruning on a large table, a big
// configuration save) finish well inside this.
const statementTimeout = 5 * time.Minute

type Database struct {
	Config         *Config
	DateTimeFormat string
	Sql            *sql.DB
	// executor, when non-nil, is an ambient transaction that all Exec/Query/
	// QueryRow calls go through. Use WithTx to acquire one; do NOT set this
	// by hand — it'd leak a half-open tx if WithTx isn't used to wrap it.
	executor dbExecutor
}

// dbExecutor is satisfied by both *sql.DB and *sql.Tx, letting Database
// transparently run inside or outside a transaction.
type dbExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func NewDatabase(config *Config) *Database {
	var err error

	database := &Database{Config: config}

	switch config.DbType {
	case DbTypeSqlite:
		database.DateTimeFormat = "2006-01-02 15:04:05.000 -07:00"

		dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout%%3d10000", config.GetDbFilePath())

		if database.Sql, err = sql.Open("sqlite", dsn); err != nil {
			log.Fatal(err)
		}

	case DbTypeMariadb, DbTypeMysql:
		database.DateTimeFormat = "2006-01-02 15:04:05"

		// readTimeout is a driver-level I/O deadline, so a server that accepts
		// a statement and then never answers can't pin the caller forever.
		// Deliberately NOT using max_execution_time: go-sql-driver turns
		// unrecognised DSN parameters into a SET of a session variable, and
		// MariaDB has no such variable (it uses max_statement_time), so that
		// would fail every connection with "Error 1193: Unknown system
		// variable". readTimeout is handled by the driver and works on both.
		// parseTime hands datetime columns back as time.Time instead of raw
		// bytes. Without it any Scan into a time.Time or sql.NullTime fails on
		// this backend — which is not a corner: it is how the plugin registry
		// reads installedAt, so plugins.read failed at startup and no plugin
		// ever loaded on MySQL or MariaDB. ParseDateTime already accepts a
		// time.Time, so the paths that scan into `any` are unaffected.
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?readTimeout=%s&parseTime=true",
			config.DbUsername, config.DbPassword, config.DbHost, config.DbPort, config.DbName, statementTimeout)

		if database.Sql, err = sql.Open("mysql", dsn); err != nil {
			log.Fatal(err)
		}

	case DbTypePostgres:
		database.DateTimeFormat = "2006-01-02 15:04:05"

		// statement_timeout is a Postgres run-time parameter; lib/pq forwards
		// any such parameter from the connection string to the backend at
		// startup. The server then cancels a statement that overruns it,
		// instead of the client waiting on it indefinitely.
		//
		// The password is quoted because an EMPTY one otherwise corrupts the
		// whole string: `password= dbname=x` reads as password swallowing the
		// next pair, the dbname falls back to the user name, and an operator
		// on trust auth gets "database <user> does not exist" pointing at
		// nothing. Quoting needs lib/pq's escapes for backslash and quote.
		password := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(config.DbPassword)
		dsn := fmt.Sprintf("host=%s port=%d user=%s password='%s' dbname=%s sslmode=disable statement_timeout=%d",
			config.DbHost, config.DbPort, config.DbUsername, password, config.DbName,
			statementTimeout.Milliseconds())

		if database.Sql, err = sql.Open("postgres", dsn); err != nil {
			log.Fatal(err)
		}

	default:
		log.Fatalf("unknown database type %s\n", config.DbType)
	}

	database.Sql.SetConnMaxLifetime(time.Minute)
	database.Sql.SetMaxIdleConns(25)
	database.Sql.SetMaxOpenConns(25)

	if err = database.migrate(); err != nil {
		log.Fatal(err)
	}

	if err = database.seed(); err != nil {
		log.Fatal(err)
	}

	if err = database.repairSequences(); err != nil {
		// Non-fatal: a desynced sequence only breaks inserts into the affected
		// table, and refusing to boot over it would be worse than running.
		log.Printf("warning: %v\n", err)
	}

	return database
}

// postgresSerialColumns lists the (table, column) pairs backed by a Postgres
// sequence, for repairSequences.
var postgresSerialColumns = [][2]string{
	{"rdioScannerAccesses", "_id"},
	{"rdioScannerApiKeys", "_id"},
	{"rdioScannerCalls", "id"},
	{"rdioScannerConfigs", "_id"},
	{"rdioScannerDirWatches", "_id"},
	{"rdioScannerDownstreams", "_id"},
	{"rdioScannerGroups", "_id"},
	{"rdioScannerLogs", "_id"},
	{"rdioScannerPlugins", "_id"},
	{"rdioScannerSystems", "_id"},
	{"rdioScannerTags", "_id"},
	{"rdioScannerTalkgroups", "_id"},
	{"rdioScannerUnits", "_id"},
}

// repairSequences realigns each Postgres sequence with the largest key already
// present in its table. No-op on SQLite and MySQL, whose autoincrement
// counters are derived from the table rather than kept beside it.
//
// Why this is needed: several writers insert an explicit key when the payload
// carries one (Systems.Write, Groups.Write, Tags.Write), and an explicit insert
// does not advance the sequence. Restoring a dump taken from another backend
// has the same effect, on a larger scale — every row arrives with its key and
// the sequence stays at 1. Either way the next sequence-assigned insert
// collides with an existing row:
//
//	pq: duplicate key value violates unique constraint "rdioScannerGroups_pkey"
//
// On Postgres that error also aborts the surrounding transaction, so a single
// collision during a configuration save takes every section down with it (see
// Admin.ConfigHandler). Realigning at startup is cheap — one indexed max() per
// table — idempotent, and self-heals databases that are already skewed.
func (db *Database) repairSequences() error {
	if db.Config.DbType != DbTypePostgres {
		return nil
	}

	repaired := []string{}
	failed := []string{}

	for _, tc := range postgresSerialColumns {
		moved, err := db.repairSequence(tc[0], tc[1])

		if err != nil {
			// One table's failure must not end the sweep. These are processed
			// in alphabetical order, so returning here left every table after
			// the failing one skewed — and rdioScannerConfigs is fourth of
			// thirteen, which is a long way from the end.
			failed = append(failed, fmt.Sprintf("%s.%s: %v", tc[0], tc[1], err))
			continue
		}

		if moved != "" {
			repaired = append(repaired, moved)
		}
	}

	if len(repaired) > 0 {
		log.Printf("realigned postgres sequences behind their table: %s\n", strings.Join(repaired, ", "))
	}

	if len(failed) > 0 {
		// Reported rather than swallowed. A sequence that could not be checked
		// is one that will collide later, in a save the operator cannot connect
		// to anything — which is precisely how this went unnoticed.
		return fmt.Errorf("repairsequences: %s", strings.Join(failed, "; "))
	}

	return nil
}

// repairSequence realigns one sequence, and reports what it changed.
//
// The alignment is unconditional for a non-empty table rather than guarded by a
// comparison against the sequence's current value, because that comparison
// cannot be made correctly from `pg_sequence_last_value` alone: it reports
// last_value without is_called, and a sequence left at (last_value = N,
// is_called = false) — which is what several restore paths produce — hands out
// N rather than N+1. Read that way the sequence looks caught up with a table
// whose highest key is N, so it was skipped as healthy on every boot, and every
// insert that reached for a new key collided with the row already holding N:
//
//	pq: duplicate key value violates unique constraint "rdioScannerConfigs_pkey"
//
// Silent, and permanent, since each boot took the same branch. setval with an
// explicit is_called leaves no such ambiguity, costs one indexed max() per
// table, and is idempotent.
func (db *Database) repairSequence(table string, column string) (string, error) {
	seq, err := db.sequenceFor(table, column)
	if err != nil {
		return "", err
	}
	if seq == "" {
		// The table is absent from this schema version — nothing to realign.
		return "", nil
	}

	var maxId sql.NullInt64

	probe := fmt.Sprintf(`select max("%s") from "%s"`, column, table)
	if err := db.Sql.QueryRow(probe).Scan(&maxId); err != nil {
		return "", fmt.Errorf("probing %s.%s: %v", table, column, err)
	}

	// Empty table: leave it alone so the first insert still gets key 1.
	if !maxId.Valid || maxId.Int64 < 1 {
		return "", nil
	}

	// Only consulted for the report at the end, so a failure here must not
	// stop the realignment — unlike max above, which the repair depends on.
	var last sql.NullInt64
	_ = db.Sql.QueryRow(
		fmt.Sprintf(`select pg_sequence_last_value('%s'::regclass)`, seq),
	).Scan(&last)

	set := fmt.Sprintf(
		`select setval('%s'::regclass, (select max("%s") from "%s"), true)`,
		seq, column, table,
	)
	if _, err := db.Sql.Exec(set); err != nil {
		// setval needs UPDATE on the sequence, and a role that was handed its
		// table privileges after someone else built the schema often does not
		// have it — that role can collide all day yet never repair. It can
		// still call nextval, though: USAGE is what its inserts already run
		// on. Walking the sequence forward is slower and burns a key, but it
		// works with exactly the privileges the failure proves are there.
		if advErr := db.advanceSequence(seq, maxId.Int64); advErr != nil {
			return "", fmt.Errorf("setval: %v (nextval fallback: %v)", err, advErr)
		}
	}

	// Only worth a line when it actually moved. The is_called case cannot be
	// told apart from a healthy sequence here, so it is reported as a repair
	// only when last_value was genuinely behind or unused.
	if last.Valid && last.Int64 >= maxId.Int64 {
		return "", nil
	}

	from := "unused"
	if last.Valid {
		from = fmt.Sprintf("%d", last.Int64)
	}

	return fmt.Sprintf("%s.%s (%s -> %d)", table, column, from, maxId.Int64), nil
}

// advanceCap bounds how far advanceSequence will walk. A sequence behind by
// more than this is a badly skewed high-traffic table (calls, after a dump
// restored with explicit keys); at one statement server-side the walk itself
// stays inside the statement timeout well past this, but burning tens of
// millions of keys to dodge a missing GRANT is the wrong trade — at that
// scale the operator is told to fix the privilege instead.
const advanceCap = 10_000_000

// advanceSequence walks a sequence forward with nextval until its next key
// clears the table's highest, for roles that can use the sequence but not
// setval it. The first nextval doubles as the position probe — one key is
// wasted when the sequence turns out healthy, which never matters here since
// this only runs after setval was refused. The remaining distance is covered
// in a single generate_series statement rather than a round trip per key.
func (db *Database) advanceSequence(seq string, maxId int64) error {
	var current int64
	if err := db.Sql.QueryRow(fmt.Sprintf(`select nextval('%s'::regclass)`, seq)).Scan(&current); err != nil {
		return err
	}

	if current >= maxId {
		return nil
	}

	gap := maxId - current
	if gap > advanceCap {
		return fmt.Errorf(
			"%s is %d keys behind, beyond the nextval walk cap of %d; grant the server's role UPDATE on the sequence (or make it the owner) and restart",
			seq, gap, advanceCap,
		)
	}

	if _, err := db.Sql.Exec(fmt.Sprintf(
		`select nextval('%s'::regclass) from generate_series(1, %d)`, seq, gap,
	)); err != nil {
		return err
	}

	return nil
}

// nextvalPattern picks the sequence out of a column default such as
// `nextval('"rdioScannerConfigs__id_seq"'::regclass)`.
var nextvalPattern = regexp.MustCompile(`nextval\('([^']+)'`)

// sequenceFor resolves the sequence that hands out table.column's keys.
//
// pg_get_serial_sequence only knows sequences OWNED by their column, and a
// schema that arrived through an external migration tool or a hand-built
// restore often has the default drawing from a sequence that ownership never
// got attached to. Feeding its NULL onward is worse than an error: setval(NULL)
// returns NULL without complaint, so the repair read as successful while
// touching nothing, and the collision came back on every save. When ownership
// is missing the sequence is read out of the column DEFAULT instead, and a
// column with neither is reported rather than skipped.
//
// An empty name with a nil error means the table itself is absent from this
// schema version, which is not a failure — there is nothing to realign.
func (db *Database) sequenceFor(table string, column string) (string, error) {
	var seq sql.NullString

	query := fmt.Sprintf(`select pg_get_serial_sequence('"%s"', '%s')`, table, column)
	if err := db.Sql.QueryRow(query).Scan(&seq); err != nil {
		return "", nil
	}

	if seq.Valid {
		return seq.String, nil
	}

	var def sql.NullString
	err := db.Sql.QueryRow(
		`select pg_get_expr(d.adbin, d.adrelid)
		   from pg_attrdef d
		   join pg_class c on c.oid = d.adrelid
		   join pg_attribute a on a.attrelid = d.adrelid and a.attnum = d.adnum
		  where c.relname = $1 and a.attname = $2`,
		table, column,
	).Scan(&def)
	if err == sql.ErrNoRows || (err == nil && !def.Valid) {
		return "", fmt.Errorf("%s.%s has no owned sequence and no column default to realign", table, column)
	}
	if err != nil {
		return "", err
	}

	match := nextvalPattern.FindStringSubmatch(def.String)
	if match == nil {
		return "", fmt.Errorf("%s.%s default %q does not draw from a sequence", table, column, def.String)
	}

	return match[1], nil
}

// isSequenceCollision reports whether an error is Postgres refusing an insert
// because the key a sequence handed out is already taken.
//
// Narrow on purpose: SQLSTATE 23505 covers every unique constraint, and a
// genuine duplicate — two accesses sharing a code, two systems sharing an id —
// is a real error the operator has to see, not something to retry silently.
// Only a collision on the table's own primary key indicates a skewed sequence.
func isSequenceCollision(err error, table string) bool {
	pqErr, ok := err.(*pq.Error)
	if !ok {
		return false
	}

	return pqErr.Code == "23505" && pqErr.Constraint == table+"_pkey"
}

// ExecInsert runs an insert whose key comes from a sequence, realigning that
// sequence and trying once more if it collides.
//
// Startup alignment is not enough on its own. A dump restored while the server
// is running, or any writer that inserts an explicit key, leaves the sequence
// behind again — and the operator meets it as a save that fails with a message
// about a constraint they have never heard of, which no amount of retrying the
// save will clear. Repairing at the point of collision means the second attempt
// succeeds and the cause is gone rather than waiting for the next restart.
//
// Inside a transaction the insert has to sit behind a savepoint. Postgres
// aborts the whole transaction at its first failed statement, so without one
// the retry could only ever come back with "current transaction is aborted" —
// which both doomed the retry and buried the duplicate-key error that actually
// explained the failure. The savepoint is rolled back on any error, retryable
// or not, so the caller's transaction stays usable and the error that reaches
// them is the insert's own.
func (db *Database) ExecInsert(table string, column string, query string, args ...any) error {
	guarded := db.Config.DbType == DbTypePostgres && db.executor != nil
	if guarded {
		if _, err := db.Exec("savepoint rdio_exec_insert"); err != nil {
			// No savepoint means no safe retry, likely because the transaction
			// is already aborted; the insert below then reports that state.
			guarded = false
		}
	}

	_, err := db.Exec(query, args...)

	if err == nil {
		if guarded {
			_, _ = db.Exec("release savepoint rdio_exec_insert")
		}
		return nil
	}

	if guarded {
		if _, spErr := db.Exec("rollback to savepoint rdio_exec_insert"); spErr != nil {
			return err
		}
	}

	if db.Config.DbType != DbTypePostgres || !isSequenceCollision(err, table) {
		return err
	}

	if db.executor != nil && !guarded {
		// In a transaction with no savepoint to rewind to, the transaction is
		// aborted: a retry cannot succeed and its error would mask this one.
		return err
	}

	if _, repairErr := db.repairSequence(table, column); repairErr != nil {
		// The original error is the one worth reporting: it is what actually
		// stopped the write, and the repair failing is a detail beneath it.
		return fmt.Errorf("%v (realigning %s.%s also failed: %v)", err, table, column, repairErr)
	}

	log.Printf("realigned the %s.%s sequence after a key collision\n", table, column)

	_, err = db.Exec(query, args...)

	return err
}

func (db *Database) ParseDateTime(f any) (time.Time, error) {
	var dateTimeStr string

	switch v := f.(type) {
	case []uint8:
		dateTimeStr = string(v)
	case string:
		dateTimeStr = v
	case time.Time:
		return v, nil
	default:
		return time.Time{}, fmt.Errorf("unknown datetime format %T", v)
	}

	// Try multiple datetime formats (database may store in different formats)
	formats := []string{
		time.RFC3339,                  // "2006-01-02T15:04:05Z07:00" - ISO 8601 with timezone
		"2006-01-02T15:04:05Z",        // ISO 8601 UTC
		"2006-01-02T15:04:05.000Z",    // ISO 8601 UTC with milliseconds
		"2006-01-02T15:04:05.999999Z", // ISO 8601 UTC with microseconds
		db.DateTimeFormat,             // Database's expected format
		"2006-01-02 15:04:05",         // MySQL standard format
		"2006-01-02 15:04:05.000",     // With milliseconds, no timezone
	}

	var lastErr error
	for _, format := range formats {
		if t, err := time.Parse(format, dateTimeStr); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse datetime '%s': %v", dateTimeStr, lastErr)
}

func (db *Database) formatQuery(query string) string {
	if db.Config.DbType != DbTypePostgres {
		return query
	}
	query = strings.ReplaceAll(query, "`", "\"")
	var result strings.Builder
	counter := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			counter++
			result.WriteString(fmt.Sprintf("$%d", counter))
		} else {
			result.WriteByte(query[i])
		}
	}
	return result.String()
}

func (db *Database) runner() dbExecutor {
	if db.executor != nil {
		return db.executor
	}
	return db.Sql
}

// slowQueryThreshold is when a statement is worth naming. Everything the server
// runs against its own tables is meant to be indexed and quick; at this
// duration something is either scanning or waiting for a free connection, and
// both are worth seeing.
const slowQueryThreshold = 2 * time.Second

// slowQueryThrottle keeps a query that is always slow from filling the log,
// while still letting distinct queries each be reported. Keyed by the shape of
// the statement, not the whole text, so the same query with different literals
// shares one budget.
var slowQueryThrottle = NewLogThrottle(3, time.Minute)

// traceSlowQuery reports a statement that took too long.
//
// Straight to the process log rather than through LogEvent: the log table is
// itself written through Exec, so routing this back through the logger would
// have a slow log insert report itself, forever. This lands in the same output
// an operator is already reading.
func traceSlowQuery(query string, started time.Time) {
	elapsed := time.Since(started)
	if elapsed < slowQueryThreshold {
		return
	}

	// The log table's own insert is exempt for the reason above.
	if strings.Contains(query, "rdioScannerLogs") {
		return
	}

	flat := strings.Join(strings.Fields(query), " ")

	key := flat
	if len(key) > 60 {
		key = key[:60]
	}
	if !slowQueryThrottle.Allow(key) {
		return
	}

	if len(flat) > 300 {
		flat = flat[:300] + "…"
	}

	log.Printf("slow query: %s — %s", elapsed.Round(time.Millisecond), flat)
}

// Exec bounds every write with statementTimeout. The deadline covers waiting
// for a pooled connection as well as the statement itself, so an exhausted
// pool (SetMaxOpenConns) surfaces as an error instead of blocking the caller
// indefinitely — which is how a single leaked *sql.Rows used to take the whole
// process down.
func (db *Database) Exec(query string, args ...any) (sql.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
	defer cancel()

	defer traceSlowQuery(query, time.Now())

	return db.runner().ExecContext(ctx, db.formatQuery(query), args...)
}

// Query and QueryRow intentionally do NOT take a Go-side deadline. The context
// would have to outlive the call — cancelling it on return (the only thing a
// wrapper this shape can do) invalidates the *sql.Rows / *sql.Row before the
// caller has scanned it. Bounding these properly means returning a wrapper
// that cancels on Close, which would touch all 34 query sites; until then the
// server-side timeouts configured on the DSN in NewDatabase are what stop a
// runaway read.
func (db *Database) Query(query string, args ...any) (*sql.Rows, error) {
	defer traceSlowQuery(query, time.Now())

	return db.runner().Query(db.formatQuery(query), args...)
}

// QueryContext is Query with a caller-supplied deadline. Only safe where the
// caller finishes with the rows before cancelling — the context must outlive
// the scan, so this is for callers that drain and close within one function,
// not for handing *sql.Rows onwards.
//
// The deadline matters more than the statement timeouts already configured on
// each DSN: those bound how long the *server* spends executing, and cover
// nothing about the wait for a free connection from the pool. That wait is
// client-side and, without a context, unbounded — so an exhausted pool parks
// the caller indefinitely with the database sitting perfectly idle.
func (db *Database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	defer traceSlowQuery(query, time.Now())

	return db.runner().QueryContext(ctx, db.formatQuery(query), args...)
}

// QueryRowContext is QueryRow with a caller-supplied deadline. Safe only where
// the row is scanned before the context is cancelled — a single-row read inside
// one function, not a row handed onwards.
//
// Same reasoning as QueryContext: the DSN's statement timeout bounds how long
// the server spends executing, and says nothing about waiting for a free
// connection. That wait is client-side and, without a context, unbounded.
func (db *Database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	defer traceSlowQuery(query, time.Now())

	return db.runner().QueryRowContext(ctx, db.formatQuery(query), args...)
}

func (db *Database) QueryRow(query string, args ...any) *sql.Row {
	defer traceSlowQuery(query, time.Now())

	return db.runner().QueryRow(db.formatQuery(query), args...)
}

// WithTx runs fn inside a database transaction. The *Database passed to fn
// has all Exec/Query/QueryRow calls routed through the transaction, so any
// existing code using the Database's own methods transparently participates.
// Commits on nil error; rolls back on error or panic.
func (db *Database) WithTx(fn func(txDb *Database) error) (err error) {
	tx, err := db.Sql.Begin()
	if err != nil {
		return err
	}
	txDb := &Database{
		Config:         db.Config,
		DateTimeFormat: db.DateTimeFormat,
		Sql:            db.Sql,
		executor:       tx,
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
		}
	}()
	if err = fn(txDb); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *Database) migrate() error {
	var (
		err     error
		verbose bool
	)

	verbose, err = db.prepareMigration()

	if err == nil {
		err = db.migration20191028144433(verbose)
	}
	if err == nil {
		err = db.migration20191029092201(verbose)
	}
	if err == nil {
		err = db.migration20191126135515(verbose)
	}
	if err == nil {
		err = db.migration20191220093214(verbose)
	}
	if err == nil {
		err = db.migration20200123094105(verbose)
	}
	if err == nil {
		err = db.migration20200428132918(verbose)
	}
	if err == nil {
		err = db.migration20210115105958(verbose)
	}
	if err == nil {
		err = db.migration20210830092027(verbose)
	}
	if err == nil {
		err = db.migration20211202094819(verbose)
	}
	if err == nil {
		err = db.migration20220101070000(verbose)
	}
	if err == nil {
		err = db.migration20260421120000(verbose)
	}
	if err == nil {
		err = db.migration20260421130000(verbose)
	}
	if err == nil {
		err = db.migration20260422140000(verbose)
	}
	if err == nil {
		err = db.migration20260422150000(verbose)
	}
	if err == nil {
		err = db.migration20260422160000(verbose)
	}
	if err == nil {
		err = db.migration20260422170000(verbose)
	}
	if err == nil {
		err = db.migration20260422180000(verbose)
	}
	if err == nil {
		err = db.migration20260424100000(verbose)
	}
	if err == nil {
		err = db.migration20260424110000(verbose)
	}
	if err == nil {
		err = db.migration20260519100000(verbose)
	}
	if err == nil {
		err = db.migration20260519110000(verbose)
	}
	if err == nil {
		err = db.migration20260519120000(verbose)
	}
	if err == nil {
		err = db.migration20260519130000(verbose)
	}
	if err == nil {
		err = db.migration20260615120000(verbose)
	}
	if err == nil {
		err = db.migration20260617120000(verbose)
	}
	if err == nil {
		err = db.migration20260801120000(verbose)
	}
	if err == nil {
		err = db.migration20260803090000(verbose)
	}
	if err == nil {
		err = db.migration20260815090000(verbose)
	}
	if err == nil {
		err = db.migration20260819100000(verbose)
	}
	if err == nil {
		err = db.migrationTranscriptsToPlugin(verbose)
	}

	return err
}

func (db *Database) migrateWithSchema(name string, schemas []string, verbose bool) error {
	var (
		count int = 0
		err   error
		query string
		tx    *sql.Tx
	)

	formatError := func(err error, query string) error {
		return fmt.Errorf("%s while doing %s", err.Error(), query)
	}

	query = db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err = db.Sql.QueryRow(query).Scan(&count); err != nil {
		return formatError(err, query)
	}

	if count == 0 {
		if verbose {
			log.Printf("running database migration %s", name)
		}

		// A failed Begin used to fall straight through to `return nil`: the
		// migration did nothing, wrote no ledger row, and reported success, so
		// startup carried on against a schema that had never been created and
		// the first query to touch it failed instead — somewhere else entirely.
		if tx, err = db.Sql.Begin(); err != nil {
			return fmt.Errorf("%s: cannot begin: %v", name, err)
		}

		for _, query = range schemas {
			if _, err = tx.Exec(db.formatQuery(query)); err != nil {
				tx.Rollback()
				return formatError(err, query)
			}
		}

		query = db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))
		if _, err = tx.Exec(query); err != nil {
			tx.Rollback()
			return formatError(err, query)
		}

		// Worth knowing what this transaction does and does not buy. MySQL and
		// MariaDB commit DDL implicitly, so a failure part way through leaves
		// the earlier statements applied and the rollback undoes nothing — the
		// reason every statement here wants to be re-runnable rather than
		// relying on the rollback to tidy up.
		if err = tx.Commit(); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %v", name, err)
		}
	}

	return nil
}

func (db *Database) migration20191028144433(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerSystems` (`id` integer primary key autoincrement, `createdAt` datetime not null, `updatedAt` datetime not null, `name` varchar(255) not null, `system` integer not null, `talkgroups` json not null)",
			"create unique index `rdio_scanner_systems_system` on `rdioScannerSystems` (`system`)",
		}
	case DbTypePostgres:
		queries = []string{
			`create table "rdioScannerSystems" ("id" serial primary key, "createdAt" timestamp not null, "updatedAt" timestamp not null, "name" varchar(255) not null, "system" integer not null, "talkgroups" text not null)`,
			`create unique index "rdio_scanner_systems_system" on "rdioScannerSystems" ("system")`,
		}
	default:
		queries = []string{
			"create table `rdioScannerSystems` (`id` integer primary key auto_increment, `createdAt` datetime not null, `updatedAt` datetime not null, `name` varchar(255) not null, `system` integer not null, `talkgroups` json not null)",
			"create unique index `rdio_scanner_systems_system` on `rdioScannerSystems` (`system`)",
		}
	}
	return db.migrateWithSchema("20191028144433-create-rdio-scanner-system", queries, verbose)
}

func (db *Database) migration20191029092201(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerCalls` (`id` integer primary key autoincrement, `createdAt` datetime not null, `updatedAt` datetime not null, `audio` longblob not null, `emergency` tinyint(1) not null, `freq` integer not null, `freqList` json not null, `startTime` datetime not null, `stopTime` datetime not null, `srcList` json not null, `system` integer not null, `talkgroup` integer not null)",
			"create index `rdio_scanner_calls_start_time` on `rdioScannerCalls` (`startTime`)",
			"create index `rdio_scanner_calls_system` on `rdioScannerCalls` (`system`)",
			"create index `rdio_scanner_calls_talkgroup` on `rdioScannerCalls` (`talkgroup`)",
		}
	case DbTypePostgres:
		queries = []string{
			`create table "rdioScannerCalls" ("id" serial primary key, "createdAt" timestamp not null, "updatedAt" timestamp not null, "audio" bytea not null, "emergency" boolean not null, "freq" integer not null, "freqList" text not null, "startTime" timestamp not null, "stopTime" timestamp not null, "srcList" text not null, "system" integer not null, "talkgroup" integer not null)`,
			`create index "rdio_scanner_calls_start_time" on "rdioScannerCalls" ("startTime")`,
			`create index "rdio_scanner_calls_system" on "rdioScannerCalls" ("system")`,
			`create index "rdio_scanner_calls_talkgroup" on "rdioScannerCalls" ("talkgroup")`,
		}
	default:
		queries = []string{
			"create table `rdioScannerCalls` (`id` integer primary key auto_increment, `createdAt` datetime not null, `updatedAt` datetime not null, `audio` longblob not null, `emergency` tinyint(1) not null, `freq` integer not null, `freqList` json not null, `startTime` datetime not null, `stopTime` datetime not null, `srcList` json not null, `system` integer not null, `talkgroup` integer not null)",
			"create index `rdio_scanner_calls_start_time` on `rdioScannerCalls` (`startTime`)",
			"create index `rdio_scanner_calls_system` on `rdioScannerCalls` (`system`)",
			"create index `rdio_scanner_calls_talkgroup` on `rdioScannerCalls` (`talkgroup`)",
		}
	}
	return db.migrateWithSchema("20191029092201-create-rdio-scanner-call", queries, verbose)
}

func (db *Database) migration20191126135515(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite, DbTypePostgres:
		queries = []string{
			"drop index `rdio_scanner_calls_system`",
			"drop index `rdio_scanner_calls_talkgroup`",
		}
	default:
		queries = []string{
			"drop index `rdio_scanner_calls_system` on `rdioScannerCalls`",
			"drop index `rdio_scanner_calls_talkgroup` on `rdioScannerCalls`",
		}
	}
	return db.migrateWithSchema("20191126135515-optimize-rdio-scanner-calls", queries, verbose)
}

func (db *Database) migration20191220093214(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerCalls" add column "audioName" varchar(255)`,
			`alter table "rdioScannerCalls" add column "audioType" varchar(255)`,
			`alter table "rdioScannerSystems" add column "aliases" text not null default '[]'`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerCalls` add column `audioName` varchar(255)",
			"alter table `rdioScannerCalls` add column `audioType` varchar(255)",
			"alter table `rdioScannerSystems` add column `aliases` json not null",
		}
	}
	return db.migrateWithSchema("20191220093214-new-v3-tables", queries, verbose)
}

func (db *Database) migration20200123094105(verbose bool) error {
	queries := []string{
		"create index `rdio_scanner_calls_system` on `rdioScannerCalls` (`system`)",
		"create index `rdio_scanner_calls_system_talkgroup` on `rdioScannerCalls` (`system`, `talkgroup`)",
	}
	return db.migrateWithSchema("20200123094105-optimize-rdio-scanner-calls", queries, verbose)
}

func (db *Database) migration20200428132918(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"drop table `rdioScannerSystems`",
			"create table `rdioScannerCalls2` (`id` integer primary key autoincrement, `audio` longblob not null, `audioName` varchar(255), `audioType` varchar(255), `dateTime` datetime not null, `frequencies` json not null, `frequency` integer, `source` integer, `sources` json not null, `system` integer not null, `talkgroup` integer not null)",
			"insert into `rdioScannerCalls2` select `id`, `audio`, `audioName`, `audioType`, `startTime`, `freqList`, `freq`, null, `srcList`, `system`, `talkgroup` from `rdioScannerCalls`",
			"drop table `rdioScannerCalls`",
			"alter table `rdioScannerCalls2` rename to `rdioScannerCalls`",
			"create index `rdio_scanner_calls_date_time_system_talkgroup` on `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`)",
		}
	case DbTypePostgres:
		queries = []string{
			`drop table "rdioScannerSystems"`,
			`create table "rdioScannerCalls2" ("id" serial primary key, "audio" bytea not null, "audioName" varchar(255), "audioType" varchar(255), "dateTime" timestamp not null, "frequencies" text not null, "frequency" integer, "source" integer, "sources" text not null, "system" integer not null, "talkgroup" integer not null)`,
			`insert into "rdioScannerCalls2" select "id", "audio", "audioName", "audioType", "startTime", "freqList", "freq", null, "srcList", "system", "talkgroup" from "rdioScannerCalls"`,
			`drop table "rdioScannerCalls"`,
			`alter table "rdioScannerCalls2" rename to "rdioScannerCalls"`,
			`create index "rdio_scanner_calls_date_time_system_talkgroup" on "rdioScannerCalls" ("dateTime", "system", "talkgroup")`,
		}
	default:
		queries = []string{
			"drop table `rdioScannerSystems`",
			"create table `rdioScannerCalls2` (`id` integer primary key auto_increment, `audio` longblob not null, `audioName` varchar(255), `audioType` varchar(255), `dateTime` datetime not null, `frequencies` json not null, `frequency` integer, `source` integer, `sources` json not null, `system` integer not null, `talkgroup` integer not null)",
			"insert into `rdioScannerCalls2` select `id`, `audio`, `audioName`, `audioType`, `startTime`, `freqList`, `freq`, null, `srcList`, `system`, `talkgroup` from `rdioScannerCalls`",
			"drop table `rdioScannerCalls`",
			"alter table `rdioScannerCalls2` rename to `rdioScannerCalls`",
			"create index `rdio_scanner_calls_date_time_system_talkgroup` on `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`)",
		}
	}
	return db.migrateWithSchema("20200428132918-new-v4-tables", queries, verbose)
}

func (db *Database) migration20210115105958(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerAccesses` (`_id` integer primary key autoincrement, `code` varchar(255) not null unique, `expiration` datetime, `ident` varchar(255), `limit` integer, `order` integer, `systems` text not null)",
			"create table `rdioScannerApiKeys` (`_id` integer primary key autoincrement, `disabled` tinyint(1) default 0, `ident` varchar(255), `key` varchar(255) not null unique, `order` integer, `systems` text not null)",
			"create table `rdioScannerCalls2` (`id` integer primary key autoincrement, `audio` longblob not null, `audioName` varchar(255), `audioType` varchar(255), `dateTime` datetime not null, `frequencies` text not null, `frequency` integer, `source` integer, `sources` text not null, `system` integer not null, `talkgroup` integer not null)",
			"create index `rdio_scanner_calls2_date_time_system_talkgroup` on `rdioScannerCalls2` (`dateTime`, `system`, `talkgroup`)",
			"insert into `rdioScannerCalls2` select `id`, `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `source`, `sources`, `system`, `talkgroup` from `rdioScannerCalls`",
			"drop table `rdioScannerCalls`",
			"alter table `rdioScannerCalls2` rename to `rdioScannerCalls`",
			"create table `rdioScannerConfigs` (`_id` integer primary key autoincrement, `key` varchar(255) not null unique, `val` text not null)",
			"create index `rdio_scanner_configs_key` on `rdioScannerConfigs` (`key`)",
			"create table `rdioScannerDirWatches` (`_id` integer primary key autoincrement, `delay` integer default 0, `deleteAfter` tinyint(1) default 0, `directory` varchar(255) not null unique, `disabled` tinyint(1) default 0, `extension` varchar(255), `frequency` integer, `mask` varchar(255), `order` integer, `systemId` integer, `talkgroupId` integer, `type` varchar(255), `usePolling` tinyint(1) default 0)",
			"create table `rdioScannerDownstreams` (`_id` integer primary key autoincrement, `apiKey` varchar(255) not null unique, `disabled` tinyint(1) default 0, `order` integer, `systems` text not null, `url` varchar(255) not null)",
			"create table `rdioScannerGroups` (`_id` integer primary key autoincrement, `label` varchar(255) not null)",
			"create table `rdioScannerLogs` (`_id` integer primary key autoincrement, `dateTime` datetime not null, `level` varchar(255) not null, `message` varchar(255) not null)",
			"create index `rdio_scanner_logs_date_time_level` on `rdioScannerLogs` (`dateTime`, `level`)",
			"create table `rdioScannerSystems` (`_id` integer primary key autoincrement, `autoPopulate` tinyint(1) default 0, `blacklists` text not null, `id` integer not null unique, `label` varchar(255) not null, `led` varchar(255), `order` integer, `talkgroups` text not null, `units` text not null)",
			"create table `rdioScannerTags` (`_id` integer primary key autoincrement, `label` varchar(255) not null)",
		}
	case DbTypePostgres:
		queries = []string{
			`create table "rdioScannerAccesses" ("_id" serial primary key, "code" varchar(255) not null unique, "expiration" timestamp, "ident" varchar(255), "limit" integer, "order" integer, "systems" text not null)`,
			`create table "rdioScannerApiKeys" ("_id" serial primary key, "disabled" boolean default false, "ident" varchar(255), "key" varchar(255) not null unique, "order" integer, "systems" text not null)`,
			`create table "rdioScannerCalls2" ("id" serial primary key, "audio" bytea not null, "audioName" varchar(255), "audioType" varchar(255), "dateTime" timestamp not null, "frequencies" text not null, "frequency" integer, "source" integer, "sources" text not null, "system" integer not null, "talkgroup" integer not null)`,
			`create index "rdio_scanner_calls2_date_time_system_talkgroup" on "rdioScannerCalls2" ("dateTime", "system", "talkgroup")`,
			`insert into "rdioScannerCalls2" select "id", "audio", "audioName", "audioType", "dateTime", "frequencies", "frequency", "source", "sources", "system", "talkgroup" from "rdioScannerCalls"`,
			`drop table "rdioScannerCalls"`,
			`alter table "rdioScannerCalls2" rename to "rdioScannerCalls"`,
			`create table "rdioScannerConfigs" ("_id" serial primary key, "key" varchar(255) not null unique, "val" text not null)`,
			`create index "rdio_scanner_configs_key" on "rdioScannerConfigs" ("key")`,
			`create table "rdioScannerDirWatches" ("_id" serial primary key, "delay" integer default 0, "deleteAfter" boolean default false, "directory" varchar(255) not null unique, "disabled" boolean default false, "extension" varchar(255), "frequency" integer, "mask" varchar(255), "order" integer, "systemId" integer, "talkgroupId" integer, "type" varchar(255), "usePolling" boolean default false)`,
			`create table "rdioScannerDownstreams" ("_id" serial primary key, "apiKey" varchar(255) not null unique, "disabled" boolean default false, "order" integer, "systems" text not null, "url" varchar(255) not null)`,
			`create table "rdioScannerGroups" ("_id" serial primary key, "label" varchar(255) not null)`,
			`create table "rdioScannerLogs" ("_id" serial primary key, "dateTime" timestamp not null, "level" varchar(255) not null, "message" varchar(255) not null)`,
			`create index "rdio_scanner_logs_date_time_level" on "rdioScannerLogs" ("dateTime", "level")`,
			`create table "rdioScannerSystems" ("_id" serial primary key, "autoPopulate" boolean default false, "blacklists" text not null, "id" integer not null unique, "label" varchar(255) not null, "led" varchar(255), "order" integer, "talkgroups" text not null, "units" text not null)`,
			`create table "rdioScannerTags" ("_id" serial primary key, "label" varchar(255) not null)`,
		}
	default:
		queries = []string{
			"create table `rdioScannerAccesses` (`_id` integer primary key auto_increment, `code` varchar(255) not null unique, `expiration` datetime, `ident` varchar(255), `limit` integer, `order` integer, `systems` text not null)",
			"create table `rdioScannerApiKeys` (`_id` integer primary key auto_increment, `disabled` tinyint(1) default 0, `ident` varchar(255), `key` varchar(255) not null unique, `order` integer, `systems` text not null)",
			"create table `rdioScannerCalls2` (`id` integer primary key auto_increment, `audio` longblob not null, `audioName` varchar(255), `audioType` varchar(255), `dateTime` datetime not null, `frequencies` text not null, `frequency` integer, `source` integer, `sources` text not null, `system` integer not null, `talkgroup` integer not null)",
			"create index `rdio_scanner_calls2_date_time_system_talkgroup` on `rdioScannerCalls2` (`dateTime`, `system`, `talkgroup`)",
			"insert into `rdioScannerCalls2` select `id`, `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `source`, `sources`, `system`, `talkgroup` from `rdioScannerCalls`",
			"drop table `rdioScannerCalls`",
			"alter table `rdioScannerCalls2` rename to `rdioScannerCalls`",
			"create table `rdioScannerConfigs` (`_id` integer primary key auto_increment, `key` varchar(255) not null unique, `val` text not null)",
			"create index `rdio_scanner_configs_key` on `rdioScannerConfigs` (`key`)",
			"create table `rdioScannerDirWatches` (`_id` integer primary key auto_increment, `delay` integer default 0, `deleteAfter` tinyint(1) default 0, `directory` varchar(255) not null unique, `disabled` tinyint(1) default 0, `extension` varchar(255), `frequency` integer, `mask` varchar(255), `order` integer, `systemId` integer, `talkgroupId` integer, `type` varchar(255), `usePolling` tinyint(1) default 0)",
			"create table `rdioScannerDownstreams` (`_id` integer primary key auto_increment, `apiKey` varchar(255) not null unique, `disabled` tinyint(1) default 0, `order` integer, `systems` text not null, `url` varchar(255) not null)",
			"create table `rdioScannerGroups` (`_id` integer primary key auto_increment, `label` varchar(255) not null)",
			"create table `rdioScannerLogs` (`_id` integer primary key auto_increment, `dateTime` datetime not null, `level` varchar(255) not null, `message` varchar(255) not null)",
			"create index `rdio_scanner_logs_date_time_level` on `rdioScannerLogs` (`dateTime`, `level`)",
			"create table `rdioScannerSystems` (`_id` integer primary key auto_increment, `autoPopulate` tinyint(1) default 0, `blacklists` text not null, `id` integer not null unique, `label` varchar(255) not null, `led` varchar(255), `order` integer, `talkgroups` text not null, `units` text not null)",
			"create table `rdioScannerTags` (`_id` integer primary key auto_increment, `label` varchar(255) not null)",
		}
	}
	return db.migrateWithSchema("20210115105958-new-v5.1-tables", queries, verbose)
}

func (db *Database) migration20210830092027(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerSystems2` (`_id` integer primary key autoincrement, `autoPopulate` tinyint(1) default 0, `blacklists` text not null, `id` integer not null unique, `label` varchar(255) not null, `led` varchar(255), `order` integer, `talkgroups` longtext not null, `units` longtext not null)",
			"insert into `rdioScannerSystems2` select `_id`, `autoPopulate`, `blacklists`, `id`, `label`, `led`, `order`, `talkgroups`, `units` from `rdioScannerSystems`",
			"drop table `rdioScannerSystems`",
			"alter table `rdioScannerSystems2` rename to `rdioScannerSystems`",
			"drop index `rdio_scanner_calls2_date_time_system_talkgroup`",
			"create index `rdio_scanner_calls_date_time_system_talkgroup` on `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`)",
		}
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerSystems" alter column "talkgroups" type text`,
			`alter table "rdioScannerSystems" alter column "units" type text`,
			`drop index "rdio_scanner_calls2_date_time_system_talkgroup"`,
			`create index "rdio_scanner_calls_date_time_system_talkgroup" on "rdioScannerCalls" ("dateTime", "system", "talkgroup")`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerSystems` modify `talkgroups` longtext null not null",
			"alter table `rdioScannerSystems` modify `units` longtext null not null",
			"drop index `rdio_scanner_calls2_date_time_system_talkgroup` on `rdioScannerCalls`",
			"create index `rdio_scanner_calls_date_time_system_talkgroup` on `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`)",
		}
	}
	return db.migrateWithSchema("20210830092027-v6.0-rename-index", queries, verbose)
}

func (db *Database) migration20211202094819(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"alter table `rdioScannerDownstreams` rename to `rdioScannerDownstreams2`",
			"create table `rdioScannerDownstreams` (`_id` integer primary key autoincrement, `apiKey` varchar(255) not null, `disabled` tinyint(1) default 0, `order` integer, `systems` text not null, `url` varchar(255) not null)",
			"insert into `rdioScannerDownstreams` select * from `rdioScannerDownstreams2`",
			"drop table `rdioScannerDownstreams2`",
		}
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerDownstreams" rename to "rdioScannerDownstreams2"`,
			`create table "rdioScannerDownstreams" ("_id" serial primary key, "apiKey" varchar(255) not null, "disabled" boolean default false, "order" integer, "systems" text not null, "url" varchar(255) not null)`,
			`insert into "rdioScannerDownstreams" select * from "rdioScannerDownstreams2"`,
			`drop table "rdioScannerDownstreams2"`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerDownstreams` rename to `rdioScannerDownstreams2`",
			"create table `rdioScannerDownstreams` (`_id` integer primary key auto_increment, `apiKey` varchar(255) not null, `disabled` tinyint(1) default 0, `order` integer, `systems` text not null, `url` varchar(255) not null)",
			"insert into `rdioScannerDownstreams` select * from `rdioScannerDownstreams2`",
			"drop table `rdioScannerDownstreams2`",
		}
	}
	return db.migrateWithSchema("20211202094819-v6.0.2-alter-table", queries, verbose)
}

func (db *Database) migration20220101070000(verbose bool) error {
	var (
		err        error
		frequency  any
		id         uint
		label      string
		led        any
		name       string
		queries    []string
		rows       *sql.Rows
		stra       string
		strb       string
		talkgroups []*Talkgroup
		units      []*Unit
	)
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerCalls2` (`id` integer primary key autoincrement, `audio` longblob not null, `audioName` varchar(255), `audioType` varchar(255), `dateTime` datetime not null, `frequencies` text not null, `frequency` integer, `patches` text not null, `source` integer, `sources` text not null, `system` integer not null, `talkgroup` integer not null)",
			"insert into `rdioScannerCalls2` select `id`, `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, '[]', `source`, `sources`, `system`, `talkgroup` from `rdioScannerCalls`",
			"drop table `rdioScannerCalls`",
			"alter table `rdioScannerCalls2` rename to `rdioScannerCalls`",
			"create index `rdio_scanner_calls_date_time_system_talkgroup` on `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`)",
			"create table `rdioScannerSystems2` (`_id` integer primary key autoincrement, `autoPopulate` tinyint(1) default 0, `blacklists` text not null, `id` integer not null unique, `label` varchar(255) not null, `led` varchar(255), `order` integer)",
			"insert into `rdioScannerSystems2` select `_id`, `autoPopulate`, `blacklists`, `id`, `label`, `led`, `order` from `rdioScannerSystems`",
			"drop table `rdioScannerSystems`",
			"alter table `rdioScannerSystems2` rename to `rdioScannerSystems`",
			"create table `rdioScannerTalkgroups` (`_id` integer primary key autoincrement, `frequency` integer, `groupId` integer not null, `id` integer not null, `label` varchar(255) not null, `led` varchar(255), `name` varchar(255) not null, `order` integer, `systemId` integer not null, `tagId` integer not null)",
			"create unique index `rdio_scanner_talkgroups_system_id_id` on `rdioScannerTalkgroups` (`systemId`, `id`)",
			"create table `rdioScannerUnits` (`_id` integer primary key autoincrement, `id` integer not null, `label` varchar(255) not null, `order` integer, `systemId` integer not null)",
			"create unique index `rdio_scanner_units_system_id_id` on `rdioScannerUnits` (`systemId`, `id`)",
		}
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerCalls" add column "patches" text not null default '[]'`,
			`alter table "rdioScannerSystems" drop column "talkgroups"`,
			`alter table "rdioScannerSystems" drop column "units"`,
			`create table "rdioScannerTalkgroups" ("_id" serial primary key, "frequency" integer, "groupId" integer not null, "id" integer not null, "label" varchar(255) not null, "led" varchar(255), "name" varchar(255) not null, "order" integer, "systemId" integer not null, "tagId" integer not null)`,
			`create unique index "rdio_scanner_talkgroups_system_id_id" on "rdioScannerTalkgroups" ("systemId", "id")`,
			`create table "rdioScannerUnits" ("_id" serial primary key, "id" integer not null, "label" varchar(255) not null, "order" integer, "systemId" integer not null)`,
			`create unique index "rdio_scanner_units_system_id_id" on "rdioScannerUnits" ("systemId", "id")`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerCalls` add column `patches` text not null",
			"alter table `rdioScannerSystems` drop column `talkgroups`",
			"alter table `rdioScannerSystems` drop column `units`",
			"create table `rdioScannerTalkgroups` (`_id` integer primary key auto_increment, `frequency` integer, `groupId` integer not null, `id` integer not null, `label` varchar(255) not null, `led` varchar(255), `name` varchar(255) not null, `order` integer, `systemId` integer not null, `tagId` integer not null)",
			"create unique index `rdio_scanner_talkgroups_system_id_id` on `rdioScannerTalkgroups` (`systemId`, `id`)",
			"create table `rdioScannerUnits` (`_id` integer primary key auto_increment, `id` integer not null, `label` varchar(255) not null, `order` integer, `systemId` integer not null)",
			"create unique index `rdio_scanner_units_system_id_id` on `rdioScannerUnits` (`systemId`, `id`)",
		}
	}
	if rows, err = db.Query("select `id`, `talkgroups`, `units` from `rdioScannerSystems`"); err == nil {
		for rows.Next() {
			if err = rows.Scan(&id, &stra, &strb); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(stra), &talkgroups); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(strb), &units); err != nil {
				break
			}
			for i, tg := range talkgroups {
				switch v := tg.Frequency.(type) {
				case uint:
					frequency = v
				default:
					frequency = "null"
				}
				label = strings.ReplaceAll(tg.Label, "'", "''")
				switch v := tg.Led.(type) {
				case string:
					led = fmt.Sprintf("'%v'", strings.ReplaceAll(v, "'", "''"))
				default:
					led = "null"
				}
				name = strings.ReplaceAll(tg.Name, "'", "''")
				tg.Order = uint(i + 1)
				queries = append(queries, fmt.Sprintf("insert into `rdioScannerTalkgroups` (`frequency`, `groupId`, `id`, `label`, `led`, `name`, `order`, `systemId`, `tagId`) values (%v, %v, %v, '%v', %v, '%v', %v, %v, %v)", frequency, tg.GroupId, tg.Id, label, led, name, tg.Order, id, tg.TagId))
			}
			for i, unit := range units {
				label = strings.ReplaceAll(unit.Label, "'", "''")
				unit.Order = uint(i + 1)
				queries = append(queries, fmt.Sprintf("insert into `rdioScannerUnits` (`id`, `label`, `order`, `systemId`) values (%v, '%v', %v, %v)", unit.Id, label, unit.Order, id))
			}
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return db.migrateWithSchema("20220101070000-v6.1.0", queries, verbose)
}

// migration20260424110000 retries the transcript trigram GIN creation. The
// earlier migration (20260422180000) tolerates a missing pg_trgm extension
// by just logging and moving on — if the DB role gained CREATE-extension
// permission since then (or you installed the extension manually), this
// migration picks up where the first attempt left off.
//
// Both CREATE statements are IF NOT EXISTS, so this is safe to run even
// when the extension and index already exist.
func (db *Database) migration20260424110000(verbose bool) error {
	const name = "20260424110000-transcript-trgm-idx-retry"

	var count int
	checkQuery := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(checkQuery).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec("create extension if not exists pg_trgm"); err != nil {
			log.Printf("%s: could not install pg_trgm extension, transcript search will fall back to seq scan: %v", name, err)
		} else {
			if _, err := db.Sql.Exec(`create index if not exists "rdio_scanner_calls_transcript_trgm" on "rdioScannerCalls" using gin ("transcript" gin_trgm_ops)`); err != nil {
				log.Printf("%s: could not create transcript trigram index: %v", name, err)
			} else if verbose {
				log.Printf("%s: transcript trigram index ensured", name)
			}
		}
	}

	if _, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		return fmt.Errorf("%s record: %v", name, err)
	}
	return nil
}

// migration20260424100000 adds a BRIN index on dateTime for Postgres. BRIN
// is the right fit for naturally-ordered, time-series columns: it's roughly
// 1% the size of a B-tree, and lets the planner skip whole heap ranges on
// dateTime filters (overview counts, stats buckets, search date pickers).
// The existing composite B-tree on (dateTime, system, talkgroup) stays —
// it's still the best index for the search ORDER BY path.
//
// Tolerant like the trigram migration: if the index can't be created we log
// and move on, and the migration is still recorded so we don't retry every
// boot.
func (db *Database) migration20260424100000(verbose bool) error {
	const name = "20260424100000-dateTime-brin-idx"

	var count int
	checkQuery := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(checkQuery).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec(`create index if not exists "rdio_scanner_calls_date_time_brin" on "rdioScannerCalls" using brin ("dateTime") with (pages_per_range = 32)`); err != nil {
			log.Printf("%s: could not create BRIN index on dateTime: %v", name, err)
		}
	}

	if _, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		return fmt.Errorf("%s record: %v", name, err)
	}
	return nil
}

// migration20260422180000 adds a GIN trigram index on the transcript column
// for Postgres, which makes transcript LIKE/ILIKE searches fast on large
// tables. The pg_trgm extension must exist first; if the DB role can't
// create extensions or install one, the migration logs and moves on so
// startup isn't blocked — transcript search then just uses a seq scan.
func (db *Database) migration20260422180000(verbose bool) error {
	const name = "20260422180000-transcript-trgm-idx"

	var count int
	checkQuery := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(checkQuery).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec("create extension if not exists pg_trgm"); err != nil {
			log.Printf("%s: could not install pg_trgm extension, transcript search will fall back to seq scan: %v", name, err)
		} else {
			if _, err := db.Sql.Exec(`create index if not exists "rdio_scanner_calls_transcript_trgm" on "rdioScannerCalls" using gin ("transcript" gin_trgm_ops)`); err != nil {
				log.Printf("%s: could not create transcript trigram index: %v", name, err)
			}
		}
	}

	// Always record the migration as complete so we don't retry every boot.
	if _, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		return fmt.Errorf("%s record: %v", name, err)
	}
	return nil
}

func (db *Database) migration20260422170000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerSystems" add column "transcriptionPrompt" text not null default ''`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerSystems` add column `transcriptionPrompt` text not null default ''",
		}
	}
	return db.migrateWithSchema("20260422170000-system-transcription-prompt", queries, verbose)
}

func (db *Database) migration20260422160000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerSystems" add column "transcribe" boolean not null default true`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerSystems` add column `transcribe` tinyint(1) not null default 1",
		}
	}
	return db.migrateWithSchema("20260422160000-system-transcribe", queries, verbose)
}

func (db *Database) migration20260422150000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerTalkgroups" add column "transcribe" boolean not null default true`,
		}
	case DbTypeSqlite:
		queries = []string{
			"alter table `rdioScannerTalkgroups` add column `transcribe` tinyint(1) not null default 1",
		}
	default:
		queries = []string{
			"alter table `rdioScannerTalkgroups` add column `transcribe` tinyint(1) not null default 1",
		}
	}
	return db.migrateWithSchema("20260422150000-talkgroup-transcribe", queries, verbose)
}

func (db *Database) migration20260422140000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`create index if not exists "rdio_scanner_calls_system_talkgroup_date_time" on "rdioScannerCalls" ("system", "talkgroup", "dateTime")`,
		}
	case DbTypeSqlite:
		queries = []string{
			"create index if not exists `rdio_scanner_calls_system_talkgroup_date_time` on `rdioScannerCalls` (`system`, `talkgroup`, `dateTime`)",
		}
	default:
		queries = []string{
			"create index `rdio_scanner_calls_system_talkgroup_date_time` on `rdioScannerCalls` (`system`, `talkgroup`, `dateTime`)",
		}
	}
	return db.migrateWithSchema("20260422140000-calls-system-talkgroup-datetime-idx", queries, verbose)
}

func (db *Database) migration20260421130000(verbose bool) error {
	const name = "20260421130000-split-option-rows"

	var count int
	query := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(query).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	tx, err := db.Sql.Begin()
	if err != nil {
		return err
	}

	var blob string
	err = tx.QueryRow(db.formatQuery("select `val` from `rdioScannerConfigs` where `key` = 'options'")).Scan(&blob)
	if err != nil && err != sql.ErrNoRows {
		tx.Rollback()
		return fmt.Errorf("%s read: %v", name, err)
	}

	if err == nil {
		var m map[string]any
		if jerr := json.Unmarshal([]byte(blob), &m); jerr == nil {
			for k, v := range m {
				raw, jerr := json.Marshal(v)
				if jerr != nil {
					continue
				}
				rowKey := "option." + k

				res, uerr := tx.Exec(db.formatQuery("update `rdioScannerConfigs` set `val` = ? where `key` = ?"), string(raw), rowKey)
				if uerr != nil {
					tx.Rollback()
					return fmt.Errorf("%s upsert %s: %v", name, k, uerr)
				}
				if n, _ := res.RowsAffected(); n == 0 {
					if _, ierr := tx.Exec(db.formatQuery("insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)"), rowKey, string(raw)); ierr != nil {
						tx.Rollback()
						return fmt.Errorf("%s insert %s: %v", name, k, ierr)
					}
				}
			}

			if _, derr := tx.Exec(db.formatQuery("delete from `rdioScannerConfigs` where `key` = 'options'")); derr != nil {
				tx.Rollback()
				return fmt.Errorf("%s cleanup: %v", name, derr)
			}
		}
	}

	if _, err := tx.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		tx.Rollback()
		return fmt.Errorf("%s record: %v", name, err)
	}

	return tx.Commit()
}

func (db *Database) migration20260421120000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerCalls" add column "transcript" text`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerCalls` add column `transcript` text",
		}
	}
	return db.migrateWithSchema("20260421120000-add-call-transcript", queries, verbose)
}

// migration20260519100000 adds a `delay` column (minutes) to both
// rdioScannerTalkgroups and rdioScannerSystems. Default 0 means no delay,
// which preserves current immediate-emit behavior.
func (db *Database) migration20260519100000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerTalkgroups" add column "delay" integer not null default 0`,
			`alter table "rdioScannerSystems" add column "delay" integer not null default 0`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerTalkgroups` add column `delay` integer not null default 0",
			"alter table `rdioScannerSystems` add column `delay` integer not null default 0",
		}
	}
	return db.migrateWithSchema("20260519100000-add-delay-columns", queries, verbose)
}

// migration20260519130000 adds the `alert` column to rdioScannerSystems.
// Provides system-level fallback for talkgroups that don't have their own
// alert preset assigned — matches upstream v7-wip's data model where both
// System.Alert and Talkgroup.Alert exist with talkgroup winning.
func (db *Database) migration20260519130000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerSystems" add column "alert" varchar(64) not null default ''`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerSystems` add column `alert` varchar(64) not null default ''",
		}
	}
	return db.migrateWithSchema("20260519130000-add-system-alert", queries, verbose)
}

// migration20260519120000 adds the `alert` column to rdioScannerTalkgroups.
// Stores the name of a preset from server/alert.go (e.g. "alert3"); empty
// string means no announcement tone before this talkgroup's audio.
func (db *Database) migration20260519120000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerTalkgroups" add column "alert" varchar(64) not null default ''`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerTalkgroups` add column `alert` varchar(64) not null default ''",
		}
	}
	return db.migrateWithSchema("20260519120000-add-talkgroup-alert", queries, verbose)
}

// migration20260819100000 adds the `led2` column to rdioScannerSystems and
// rdioScannerTalkgroups for the dual-color LED feature (#10). Nullable with no
// default, exactly like `led`: null means no second color, and a database
// shared with a stock chuot/rdio-scanner build keeps working since stock code
// never selects the column.
func (db *Database) migration20260819100000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypePostgres:
		queries = []string{
			`alter table "rdioScannerSystems" add column "led2" varchar(255)`,
			`alter table "rdioScannerTalkgroups" add column "led2" varchar(255)`,
		}
	default:
		queries = []string{
			"alter table `rdioScannerSystems` add column `led2` varchar(255)",
			"alter table `rdioScannerTalkgroups` add column `led2` varchar(255)",
		}
	}
	return db.migrateWithSchema("20260819100000-add-led2-columns", queries, verbose)
}

// migration20260519110000 creates the rdioScannerDelayed table used by the
// Delayer to persist queued calls across server restarts. callId references
// rdioScannerCalls.id; timestamp is the unix-millisecond moment at which the
// call should be emitted to clients/downstreams.
func (db *Database) migration20260519110000(verbose bool) error {
	var queries []string
	switch db.Config.DbType {
	case DbTypeSqlite:
		queries = []string{
			"create table `rdioScannerDelayed` (`callId` integer primary key, `timestamp` integer not null)",
		}
	case DbTypePostgres:
		queries = []string{
			`create table "rdioScannerDelayed" ("callId" integer primary key, "timestamp" bigint not null)`,
		}
	default:
		queries = []string{
			"create table `rdioScannerDelayed` (`callId` integer primary key, `timestamp` bigint not null)",
		}
	}
	return db.migrateWithSchema("20260519110000-create-delayed-table", queries, verbose)
}

// migration20260803090000 adds the `commit` column that the MySQL and MariaDB
// branch of the create-plugins-table migration left out.
//
// SQLite and Postgres declared it; the default branch did not, and nothing
// added it afterwards. Plugins.Read selects `commit`, so on those two backends
// the read failed on every boot and Controller.Start skipped the entire plugin
// block — no auto-install, no plugins started, no plugins.ready, one log line.
// 6.14 moved transcription out of the server into a plugin, so what an operator
// actually saw was transcripts disappearing after an upgrade with nothing
// explaining why.
func (db *Database) migration20260803090000(verbose bool) error {
	const name = "20260803090000-plugins-commit-column"

	if done, err := db.migrationDone(name); err != nil || done {
		return err
	}

	// Absent is the case to fix; a failed probe must not be mistaken for it,
	// or this records itself complete having done nothing.
	present, err := db.columnPresent("rdioScannerPlugins", "commit")
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}

	if !present {
		if verbose {
			log.Printf("running database migration %s", name)
		}

		query := db.formatQuery("alter table `rdioScannerPlugins` add column `commit` varchar(64)")
		if _, err := db.Sql.Exec(query); err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
	}

	return db.recordMigration(name)
}

// pluginsTableSql is the registry schema, one variant per backend.
//
// Split out of the migration so a test can read all three without a server of
// each kind. The MySQL branch shipped missing a column that the other two had,
// which killed the plugin subsystem outright on that backend, and no test could
// have caught it while the SQL lived inline in a method that needs a live
// database to call.
func pluginsTableSql(dbType string) []string {
	switch dbType {
	case DbTypeSqlite:
		return []string{
			"create table if not exists `rdioScannerPlugins` (`_id` integer primary key autoincrement, `pluginId` varchar(32) not null unique, `name` text, `version` varchar(32), `source` text, `branch` varchar(255), `enabled` boolean not null default 0, `installedAt` datetime, `manifest` text, `commit` varchar(64))",
		}

	case DbTypePostgres:
		return []string{
			`create table if not exists "rdioScannerPlugins" ("_id" serial primary key, "pluginId" varchar(32) not null unique, "name" text, "version" varchar(32), "source" text, "branch" varchar(255), "enabled" boolean not null default false, "installedAt" timestamptz, "manifest" text, "commit" varchar(64))`,
		}

	default:
		return []string{
			"create table if not exists `rdioScannerPlugins` (`_id` integer not null auto_increment, `pluginId` varchar(32) not null, `name` text, `version` varchar(32), `source` text, `branch` varchar(255), `enabled` boolean not null default 0, `installedAt` datetime, `manifest` text, `commit` varchar(64), primary key (`_id`), unique key `rdio_scanner_plugins_plugin_id` (`pluginId`))",
		}
	}
}

// pluginRegistryColumns is what Plugins.Read selects, and therefore what every
// backend's schema has to provide.
var pluginRegistryColumns = []string{
	"_id", "pluginId", "name", "version", "source", "branch",
	"enabled", "installedAt", "manifest", "commit",
}

// migration20260801120000 creates the plugin registry. One row per installed
// plugin; the plugin's own tables are created separately from its manifest and
// namespaced under `plugin_<id>_`.
//
// manifest holds a copy of the plugin.json that was installed. It is redundant
// with the copy on disk by design: it lets the admin panel still describe a
// plugin whose files have gone missing, and tells the purge action which tables
// to drop when the manifest is no longer readable.
func (db *Database) migration20260801120000(verbose bool) error {
	return db.migrateWithSchema("20260801120000-create-plugins-table", pluginsTableSql(db.Config.DbType), verbose)
}

// listenersTableSql — one row per listener-count sample, written once a
// minute by the Controller's sampler. `timestamp` is epoch seconds and the
// primary key: it doubles as the range/prune index, and an epoch integer
// sidesteps the per-backend datetime formats entirely. No serial column, so
// this table stays out of postgresSerialColumns on purpose.
//
// Split out of the migration for the same reason as pluginsTableSql: the
// parity test can compare all three backends without a live server of each.
func listenersTableSql(dbType string) []string {
	switch dbType {
	case DbTypeSqlite:
		return []string{
			"create table if not exists `rdioScannerListeners` (`timestamp` integer primary key, `count` integer not null)",
		}

	case DbTypePostgres:
		return []string{
			`create table if not exists "rdioScannerListeners" ("timestamp" bigint primary key, "count" integer not null)`,
		}

	default:
		return []string{
			"create table if not exists `rdioScannerListeners` (`timestamp` bigint primary key, `count` integer not null)",
		}
	}
}

// migration20260815090000 creates the listener-count history table for the
// stats charts (issue #8).
func (db *Database) migration20260815090000(verbose bool) error {
	return db.migrateWithSchema("20260815090000-create-listeners-table", listenersTableSql(db.Config.DbType), verbose)
}

// migration20260615120000 adds indexes that make the admin logs page filters
// fast. The original (dateTime, level) index orders well but doesn't help when
// filtering by a sparse level or by message text:
//   - (level, dateTime) lets a level filter seek straight to the level and walk
//     dateTime in order — fast filter + ORDER BY + LIMIT, and fast counts.
//   - On Postgres, a GIN trigram index on message makes the category
//     (LIKE 'prefix%') and free-text (LIKE '%substr%') message filters
//     index-backed instead of a sequential scan.
//
// Tolerant like the trigram/BRIN migrations: index failures are logged and the
// migration is still recorded so we don't retry every boot.
func (db *Database) migration20260615120000(verbose bool) error {
	const name = "20260615120000-logs-filter-indexes"

	var count int
	checkQuery := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(checkQuery).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	// (level, dateTime) composite — all database types.
	var levelIdx string
	if db.Config.DbType == DbTypePostgres {
		levelIdx = `create index if not exists "rdio_scanner_logs_level_date_time" on "rdioScannerLogs" ("level", "dateTime")`
	} else {
		levelIdx = "create index if not exists `rdio_scanner_logs_level_date_time` on `rdioScannerLogs` (`level`, `dateTime`)"
	}
	if _, err := db.Sql.Exec(levelIdx); err != nil {
		log.Printf("%s: could not create (level, dateTime) index: %v", name, err)
	} else if verbose {
		log.Printf("%s: (level, dateTime) index ensured", name)
	}

	// Postgres GIN trigram on message for category/free-text log filters.
	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec("create extension if not exists pg_trgm"); err != nil {
			log.Printf("%s: could not install pg_trgm extension, log message search will fall back to seq scan: %v", name, err)
		} else if _, err := db.Sql.Exec(`create index if not exists "rdio_scanner_logs_message_trgm" on "rdioScannerLogs" using gin ("message" gin_trgm_ops)`); err != nil {
			log.Printf("%s: could not create message trigram index: %v", name, err)
		} else if verbose {
			log.Printf("%s: message trigram index ensured", name)
		}
	}

	if _, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		return fmt.Errorf("%s record: %v", name, err)
	}
	return nil
}

// migration20260617120000 runs ANALYZE on rdioScannerLogs (Postgres) so the
// planner has fresh stats for the indexes added in earlier migrations
// (rdio_scanner_logs_level_date_time and the message trigram). Without current
// stats the planner mis-costed the logs filters and fell back to slow scans.
// This is mainly for instances upgrading with existing data; a fresh install's
// table is empty, so ANALYZE is a no-op there.
//
// Tolerant: failure is logged and the migration is still recorded.
func (db *Database) migration20260617120000(verbose bool) error {
	const name = "20260617120000-logs-analyze"

	var count int
	checkQuery := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(checkQuery).Scan(&count); err != nil {
		return fmt.Errorf("%s check: %v", name, err)
	}
	if count > 0 {
		return nil
	}

	if verbose {
		log.Printf("running database migration %s", name)
	}

	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec(`analyze "rdioScannerLogs"`); err != nil {
			log.Printf("%s: could not analyze rdioScannerLogs: %v", name, err)
		}
	}

	if _, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name))); err != nil {
		return fmt.Errorf("%s record: %v", name, err)
	}
	return nil
}

func (db *Database) prepareMigration() (bool, error) {
	var (
		err     error
		verbose bool = true
	)

	if _, err = db.Exec("select count(*) as count from `rdioScannerMeta`"); err != nil {
		if _, err = db.Exec("select count(*) as count from `SequelizeMeta`"); err == nil {
			log.Println("Preparing for database migration")
			_, err = db.Exec("alter table `SequelizeMeta` rename to `rdioScannerMeta`")
		} else {
			verbose = false
			_, err = db.Exec("create table `rdioScannerMeta` (name varchar(255) not null unique primary key)")
		}
	}

	return verbose, err
}

func (db *Database) seed() error {
	if err := db.seedGroups(); err != nil {
		return err
	}

	if err := db.seedTags(); err != nil {
		return err
	}

	return nil
}

func (db *Database) seedGroups() error {
	var count uint

	formatError := func(err error) error {
		return fmt.Errorf("database.seedgroups: %s", err.Error())
	}

	if err := db.QueryRow("select count(*) from `rdioScannerGroups`").Scan(&count); err != nil {
		return formatError(err)
	}

	if count == 0 {
		if tx, err := db.Sql.Begin(); err == nil {
			for _, group := range defaults.groups {
				if _, err := tx.Exec(db.formatQuery("insert into `rdioScannerGroups` (`label`) values (?)"), group); err != nil {
					tx.Rollback()
					return formatError(err)
				}
			}

			if err := tx.Commit(); err != nil {
				return formatError(err)
			}

		} else {
			return formatError(err)
		}
	}

	return nil
}

func (db *Database) seedTags() error {
	var count uint

	formatError := func(err error) error {
		return fmt.Errorf("database.seedtags: %s", err.Error())
	}

	if err := db.QueryRow("select count(*) from `rdioScannerTags`").Scan(&count); err != nil {
		return formatError(err)
	}

	if count == 0 {
		if tx, err := db.Sql.Begin(); err == nil {
			for _, group := range defaults.tags {
				if _, err := tx.Exec(db.formatQuery("insert into `rdioScannerTags` (`label`) values (?)"), group); err != nil {
					tx.Rollback()
					return formatError(err)
				}
			}

			if err := tx.Commit(); err != nil {
				return formatError(err)
			}

		} else {
			return formatError(err)
		}
	}

	return nil
}

# Running the tests against a real database

Everything runs on SQLite by default, in a temporary file, with no setup.

That is also how the plugin registry shipped broken on MySQL for months. The
schema is written out per backend, side by side, where a missing column reads as
formatting — and nothing ever executed the MySQL or Postgres branch, so two of
the three supported backends were verified by reading them.

Two things close that, and they are not substitutes for each other:

- `TestEveryBackendDeclaresTheSameColumns` parses `database.go` and checks that
  when one migration creates a table on several backends, every backend declares
  the same columns. It needs no server and catches the exact bug that shipped.
- Pointing the suite at a real server catches what static checking cannot: a
  statement that backend will not accept, a type it renders differently, a
  constraint it enforces sooner.

## Pointing the suite at a server

```sh
RDIO_TEST_DB_TYPE=postgresql \
RDIO_TEST_DB_HOST=localhost \
RDIO_TEST_DB_PORT=5432 \
RDIO_TEST_DB_NAME=rdio_test \
RDIO_TEST_DB_USER=rdio \
RDIO_TEST_DB_PASS=secret \
go test ./server -run 'Migration|Plugin|Transcript|Schema' -v
```

`RDIO_TEST_DB_TYPE` accepts `postgresql`, `mysql` or `mariadb`. Host defaults to
`localhost` and port to 5432 or 3306 to match the type; the rest are taken as
given.

**The named database is emptied at the start of every test.** Every table
beginning `rdioScanner` or `plugin_` is dropped. Use a database kept for testing
and nothing else — never point this at a server holding calls.

**These tests SKIP, silently, when the variables are not set.** A green run on a
machine with no Postgres proves nothing about Postgres — that is how a probe
query that no Postgres has ever accepted (`select max("_id")` with no FROM)
shipped in three consecutive releases while the suite stayed green. If a change
touches SQL that runs on Postgres, run the suite against one; `go test -v` says
`SKIP` next to every test that did not really run.

## A disposable Postgres, without an install

The EDB binaries zip runs from any directory, needs no elevation and no
service. From an empty scratch directory:

```sh
curl -sLo pg.zip https://get.enterprisedb.com/postgresql/postgresql-16.4-1-windows-x64-binaries.zip
tar -xf pg.zip 2>/dev/null || powershell -c "Expand-Archive pg.zip ."
./pgsql/bin/initdb -D pgdata -U rdio -A trust -E UTF8
./pgsql/bin/pg_ctl -D pgdata -o "-p 5433" -l pglog.txt start
./pgsql/bin/createdb -p 5433 -U rdio rdio_test
```

Then run the suite with `RDIO_TEST_DB_PORT=5433` and `RDIO_TEST_DB_PASS=`
(trust auth ignores it). `pg_ctl -D pgdata stop` and delete the directory when
done — nothing registers or persists outside it.

## What to run

`-run 'Migration|Plugin|Transcript|Schema'` covers the parts where the backend
matters: the migration ledger, the plugin registry and its schema evolution, and
the transcripts move. The rest of the suite does not touch the database and has
nothing to gain from a real one.

## Race detection

```sh
cd server && CGO_ENABLED=1 go test -race ./...
```

The whole suite runs under it in well under a minute, so there is no reason to
narrow it with `-run`.

It needs a C toolchain, which is the only reason it does not run everywhere.
On Windows `scoop install gcc` is enough and needs no elevation; on Debian or
Ubuntu, `build-essential`. Nothing else has to change — `CGO_ENABLED=1` on the
command line is all it takes, and release builds stay `CGO_ENABLED=0`.

Worth running before a release because most of the plugin system is concurrent:
dispatch runs handlers while ingest writes, `call.emit` fans out per listener,
sockets and timers deliver onto event loops, and the registry is read from all
of them. Several defects found in the 6.14 sweep were exactly this shape — a
value handed to two plugins by reference, then written by both.

A green run proves less than it looks like, though: the detector only sees code
that actually ran concurrently during the tests. The tests that make it earn its
keep are the ones that deliberately race — `TestClonedCallValueIsSafeAgainstConcurrentCoreAccess`
above all, which fails with a genuine `WARNING: DATA RACE` the moment the
`[]map[string]any` case is taken back out of `clonePluginValue`. If you add
concurrent machinery, add a test that drives it from two goroutines, or the
detector will keep reporting nothing.

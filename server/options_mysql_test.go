// Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import "testing"

// MySQL and MariaDB return zero affected rows when an UPDATE writes the value
// already stored. Options.Write must not interpret that as a missing key and
// attempt a duplicate INSERT. The real-backend test is opt-in through the same
// RDIO_TEST_DB_* environment variables as the other database integration tests.
func TestOptionsWriteKeepsAnUnchangedMysqlConfigRow(t *testing.T) {
	config := testDatabaseConfigFromEnv(t, false)
	if config == nil || (config.DbType != DbTypeMysql && config.DbType != DbTypeMariadb) {
		t.Skip("needs RDIO_TEST_DB_TYPE=mysql or mariadb pointed at a disposable database")
	}

	db := NewDatabase(config)
	emptyTestDatabase(t, db)
	db.Sql.Close()
	db = NewDatabase(config)
	t.Cleanup(func() {
		db.Sql.Close()
	})

	options := NewOptions()
	options.adminPassword = "unchanged-password-hash"
	options.adminPasswordNeedChange = false

	raw := `"unchanged-password-hash"`
	if _, err := db.Exec(
		"insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)",
		"adminPassword", raw,
	); err != nil {
		t.Fatalf("cannot plant existing adminPassword: %v", err)
	}

	if err := options.Write(db); err != nil {
		t.Fatalf("unchanged existing option was treated as missing: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"select count(*) from `rdioScannerConfigs` where `key` = ?", "adminPassword",
	).Scan(&count); err != nil {
		t.Fatalf("cannot count adminPassword rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("adminPassword row count = %d; want 1", count)
	}
}

/*
 * Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>
 */

/*
 * Run with: npm test
 *
 * The CSV helpers back the talkgroup/unit export -> spreadsheet ->
 * re-import round trip. Quoting bugs here don't error — they silently
 * corrupt labels ("Fire, Rescue" splitting into two columns), which is why
 * the round trip is asserted rather than individual behaviors alone.
 */

import assert from 'node:assert/strict';
import test from 'node:test';

import { parseCsv, slugify, toCsv } from './csv.ts';

test('fields containing commas, quotes, and newlines survive a round trip', () => {
    const rows = [
        ['system', 'id', 'label'],
        ['Metro "South", 1', '101', 'Fire, Rescue'],
        ['Plain', '102', 'Multi\nline'],
    ];
    assert.deepEqual(parseCsv(toCsv(rows)), rows);
});

test('exported CSV carries a BOM and the parser strips it', () => {
    const text = toCsv([['id', 'label']]);
    assert.equal(text.charCodeAt(0), 0xfeff);
    assert.deepEqual(parseCsv(text), [['id', 'label']]);
});

test('LF and CRLF line endings both parse', () => {
    assert.deepEqual(parseCsv('a,b\nc,d'), [['a', 'b'], ['c', 'd']]);
    assert.deepEqual(parseCsv('a,b\r\nc,d\r\n'), [['a', 'b'], ['c', 'd']]);
});

test('fully empty lines are dropped, empty fields are kept', () => {
    assert.deepEqual(parseCsv('a,,c\n\n,,\nd,e,f\n'), [['a', '', 'c'], ['d', 'e', 'f']]);
});

test('null and undefined cells serialize as empty fields', () => {
    assert.equal(toCsv([['a', null, undefined, 0]]), '﻿a,,,0\r\n');
});

test('embedded quotes are doubled on the way out', () => {
    assert.equal(toCsv([['say "hi"']]), '﻿"say ""hi"""\r\n');
});

test('slugify produces filename-safe scope names', () => {
    assert.equal(slugify('Metro South 3 - Sydney South West'), 'metro-south-3-sydney-south-west');
    assert.equal(slugify('Sûreté (QC)!'), 's-ret-qc');
    assert.equal(slugify('---'), 'export');
});

test('a stray quote mid-field stays literal instead of cascading', () => {
    assert.deepEqual(parseCsv('a,5" RADIO,b\nc,d,e'), [['a', '5" RADIO', 'b'], ['c', 'd', 'e']]);
});

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
 * The merge semantics are the contract behind "export, rename in a
 * spreadsheet, re-import": existing ids must update in place with their
 * order preserved (or the form lights up duplicate-id errors), new ids
 * must append after the current maximum, and dangling group/tag ids must
 * never be produced (the server stores them silently and the scanner then
 * hides those talkgroups).
 */

import assert from 'node:assert/strict';
import test from 'node:test';

import { importTalkgroups, importUnits } from './import-merge.ts';

const row = (over = {}) => ({
    system: '', id: 101, label: 'TG 101', name: '', group: '', tag: '',
    frequency: '', led: '', delay: '', alert: '',
    ...over,
});

const baseConfig = () => ({
    groups: [{ _id: 1, label: 'Fire' }],
    tags: [{ _id: 1, label: 'Dispatch' }],
    systems: [{
        id: 1,
        label: 'Metro',
        talkgroups: [
            { id: 101, label: 'Old A', name: 'Old A Name', groupId: 1, tagId: 1, order: 1 },
            { id: 102, label: 'Old B', name: 'Old B Name', groupId: 1, tagId: 1, order: 2 },
        ],
        units: [{ id: 7, label: 'Unit 7', order: 1 }],
    }],
});

test('merging into an existing system renames in place and keeps order', () => {
    const config = baseConfig();
    const err = importTalkgroups(config, [row({ id: 101, label: 'New A', name: 'New A Name' })],
        { kind: 'system', system: { id: 1 } });

    assert.equal(err, null);
    const tgs = config.systems[0].talkgroups;
    assert.equal(tgs.length, 2);
    assert.equal(tgs[0].label, 'New A');
    assert.equal(tgs[0].order, 1);
    // Empty cells keep existing values.
    assert.equal(tgs[0].groupId, 1);
    assert.equal(tgs[0].tagId, 1);
});

test('unknown ids append after the current maximum order', () => {
    const config = baseConfig();
    importTalkgroups(config, [row({ id: 103, label: 'TG 103', group: 'Fire', tag: 'Dispatch' })],
        { kind: 'system', system: { id: 1 } });

    const tgs = config.systems[0].talkgroups;
    assert.equal(tgs.length, 3);
    assert.equal(tgs[2].order, 3);
    // Empty name falls back to the label — name is required by the form.
    assert.equal(tgs[2].name, 'TG 103');
});

test('missing groups and tags are auto-created, never left dangling', () => {
    const config = baseConfig();
    importTalkgroups(config, [row({ id: 103, group: 'EMS', tag: 'Tac' })],
        { kind: 'system', system: { id: 1 } });

    assert.deepEqual(config.groups.map((g) => g.label), ['Fire', 'EMS']);
    assert.deepEqual(config.tags.map((t) => t.label), ['Dispatch', 'Tac']);
    const added = config.systems[0].talkgroups[2];
    assert.equal(added.groupId, 2);
    assert.equal(added.tagId, 2);
});

test('inserts with empty group/tag cells land in Unknown/Untagged', () => {
    const config = baseConfig();
    importTalkgroups(config, [row({ id: 103 })], { kind: 'system', system: { id: 1 } });

    const added = config.systems[0].talkgroups[2];
    const group = config.groups.find((g) => g._id === added.groupId);
    const tag = config.tags.find((t) => t._id === added.tagId);
    assert.equal(group.label, 'Unknown');
    assert.equal(tag.label, 'Untagged');
});

test('new-system target prefills the next free system id', () => {
    const config = baseConfig();
    importTalkgroups(config, [row()], { kind: 'newSystem' });

    assert.equal(config.systems.length, 2);
    assert.equal(config.systems[0].id, 2);
    assert.equal(config.systems[0].label, undefined);
    assert.equal(config.systems[0].talkgroups.length, 1);
});

test('group target routes rows by system label and forces the group', () => {
    const config = baseConfig();
    config.systems.push({ id: 2, label: 'Rural', talkgroups: [] });

    importTalkgroups(config, [
        row({ id: 101, system: 'Metro', label: 'Renamed', tag: 'Tac' }),
        row({ id: 500, system: 'Rural', label: 'Rural TG' }),
    ], { kind: 'group', group: { _id: 9, label: 'Interop' } });

    const interop = config.groups.find((g) => g.label === 'Interop');
    assert.ok(interop, 'target group auto-created');
    // Forced axis wins on both merge and insert; the other axis honors the CSV.
    assert.equal(config.systems[0].talkgroups[0].groupId, interop._id);
    assert.equal(config.systems[0].talkgroups[0].tagId, config.tags.find((t) => t.label === 'Tac')._id);
    assert.equal(config.systems[1].talkgroups[0].groupId, interop._id);
});

test('unit merge updates labels in place and appends with continuing order', () => {
    const config = baseConfig();
    const err = importUnits(config, [
        { system: '', id: 7, label: 'Renamed 7' },
        { system: '', id: 8, label: 'New 8' },
    ], { kind: 'system', systemId: 1 });

    assert.equal(err, null);
    const units = config.systems[0].units;
    assert.equal(units.length, 2);
    assert.equal(units[0].label, 'Renamed 7');
    assert.equal(units[0].order, 1);
    assert.equal(units[1].order, 2);
});

test('unit import survives a system with no units array', () => {
    const config = baseConfig();
    delete config.systems[0].units;

    const err = importUnits(config, [{ system: '', id: 1, label: 'U1' }], { kind: 'system', systemId: 1 });
    assert.equal(err, null);
    assert.equal(config.systems[0].units.length, 1);
});

test('a vanished target system reports an error instead of silently no-oping', () => {
    const config = baseConfig();
    assert.equal(importTalkgroups(config, [row()], { kind: 'system', system: { id: 99 } }), 'Target system no longer exists');
    assert.equal(importUnits(config, [{ system: '', id: 1, label: 'U' }], { kind: 'system', systemId: 99 }), 'Target system no longer exists');
});

test('empty label cells keep the existing label on merge', () => {
    const config = baseConfig();
    importTalkgroups(config, [row({ id: 101, label: '', name: '' })], { kind: 'system', system: { id: 1 } });

    assert.equal(config.systems[0].talkgroups[0].label, 'Old A');
    assert.equal(config.systems[0].talkgroups[0].name, 'Old A Name');
});

test('routed target sends each talkgroup row to its own system', () => {
    const config = baseConfig();
    config.systems.push({ id: 2, label: 'Rural', talkgroups: [] });

    importTalkgroups(config, [
        row({ id: 101, system: 'Metro', label: 'Metro Renamed' }),
        row({ id: 500, system: 'Rural', label: 'Rural TG', group: 'Fire', tag: 'Dispatch' }),
    ], { kind: 'routed' });

    assert.equal(config.systems[0].talkgroups[0].label, 'Metro Renamed');
    // No forced axis: Metro's existing group/tag survive the merge.
    assert.equal(config.systems[0].talkgroups[0].groupId, 1);
    assert.equal(config.systems[1].talkgroups.length, 1);
    assert.equal(config.systems[1].talkgroups[0].label, 'Rural TG');
});

test('routed target sends each unit row to its own system', () => {
    const config = baseConfig();
    config.systems.push({ id: 2, label: 'Rural', units: [{ id: 7, label: 'Rural 7', order: 1 }] });

    const err = importUnits(config, [
        { system: 'Metro', id: 7, label: 'Metro 7 Renamed' },
        { system: 'Rural', id: 7, label: 'Rural 7 Renamed' },
    ], { kind: 'routed' });

    assert.equal(err, null);
    // Same unit id in two systems stays two units, each renamed in place.
    assert.equal(config.systems[0].units[0].label, 'Metro 7 Renamed');
    assert.equal(config.systems[1].units[0].label, 'Rural 7 Renamed');
});

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

import {
    importTalkgroups, importUnits, previewTalkgroups, previewUnits,
} from './import-merge.ts';

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

/*
 * Keep vs replace, and the preview that promises which one a row will get.
 *
 * The preview is the only thing telling the user what an import is about to
 * overwrite, so its verdict has to be the same one the import acts on — the
 * tests below check both halves against the same config rather than
 * checking the preview against itself.
 */

test('keep mode leaves matching talkgroups untouched and still adds new ones', () => {
    const config = baseConfig();
    const err = importTalkgroups(config, [
        row({ id: 101, label: 'New A', name: 'New A Name', group: 'EMS' }),
        row({ id: 103, label: 'TG 103' }),
    ], { kind: 'system', system: { id: 1 } }, 'keep');

    assert.equal(err, null);
    const tgs = config.systems[0].talkgroups;
    assert.equal(tgs.length, 3);
    assert.equal(tgs[0].label, 'Old A');
    assert.equal(tgs[0].name, 'Old A Name');
    assert.equal(tgs[2].label, 'TG 103');
    // The skipped row's group must not be created either — a keep-mode
    // import that grows the group list has changed something it promised
    // not to. ('Unknown' is there for the new row's empty group cell, which
    // is the insert path doing its usual job.)
    assert.equal(config.groups.some((g) => g.label === 'EMS'), false);
    assert.deepEqual(config.groups.map((g) => g.label), ['Fire', 'Unknown']);
});

test('replace mode is the default, so the round trip is unchanged', () => {
    const config = baseConfig();
    importTalkgroups(config, [row({ id: 101, label: 'New A' })], { kind: 'system', system: { id: 1 } });

    assert.equal(config.systems[0].talkgroups[0].label, 'New A');
});

test('keep mode leaves matching units untouched', () => {
    const config = baseConfig();
    importUnits(config, [
        { system: '', id: 7, label: 'Renamed' },
        { system: '', id: 8, label: 'Unit 8' },
    ], { kind: 'system', systemId: 1 }, 'keep');

    const units = config.systems[0].units;
    assert.equal(units.length, 2);
    assert.equal(units[0].label, 'Unit 7');
    assert.equal(units[1].label, 'Unit 8');
});

test('the preview marks each row new or existing, and counts them', () => {
    const config = baseConfig();
    const preview = previewTalkgroups(config, [
        row({ id: 101 }), row({ id: 103 }), row({ id: 102 }),
    ], { kind: 'system', system: { id: 1 } });

    assert.deepEqual(preview.statuses, ['existing', 'new', 'existing']);
    assert.equal(preview.existing, 2);
    assert.equal(preview.new, 1);
    assert.equal(preview.unrouted, 0);
});

test('every row is new when the target system does not exist yet', () => {
    const config = baseConfig();

    assert.deepEqual(
        previewTalkgroups(config, [row({ id: 101 })], { kind: 'newSystem' }).statuses,
        ['new'],
    );
    // A target id that has since gone reads the same way rather than
    // claiming rows would be replaced in a system that is not there.
    assert.deepEqual(
        previewTalkgroups(config, [row({ id: 101 })], { kind: 'system', system: { id: 99 } }).statuses,
        ['new'],
    );
});

test('a routed row naming an unconfigured system is reported as unrouted', () => {
    const config = baseConfig();
    const preview = previewTalkgroups(config, [
        row({ system: 'Metro', id: 101 }),
        row({ system: 'Metro', id: 103 }),
        row({ system: 'Nowhere', id: 104 }),
    ], { kind: 'routed' });

    assert.deepEqual(preview.statuses, ['existing', 'new', 'unrouted']);
    assert.equal(preview.unrouted, 1);
});

test('the preview agrees with what the import actually does', () => {
    const rows = [row({ id: 101, label: 'New A' }), row({ id: 103, label: 'TG 103' })];
    const target = { kind: 'system', system: { id: 1 } };

    const preview = previewTalkgroups(baseConfig(), rows, target);

    const kept = baseConfig();
    importTalkgroups(kept, rows, target, 'keep');
    const replaced = baseConfig();
    importTalkgroups(replaced, rows, target, 'replace');

    rows.forEach((r, i) => {
        const before = baseConfig().systems[0].talkgroups.find((tg) => tg.id === r.id);
        const afterKeep = kept.systems[0].talkgroups.find((tg) => tg.id === r.id);
        const afterReplace = replaced.systems[0].talkgroups.find((tg) => tg.id === r.id);

        if (preview.statuses[i] === 'existing') {
            // Promised a replacement: keep mode preserves it, replace mode
            // changes it.
            assert.equal(afterKeep.label, before.label);
            assert.equal(afterReplace.label, r.label);
        } else {
            // Promised a new row: it did not exist, and both modes add it.
            assert.equal(before, undefined);
            assert.equal(afterKeep.label, r.label);
            assert.equal(afterReplace.label, r.label);
        }
    });
});

test('the unit preview resolves the same targets as the unit import', () => {
    const config = baseConfig();
    const rows = [{ system: 'Metro', id: 7, label: 'Renamed' }, { system: 'Metro', id: 8, label: 'Unit 8' }];

    assert.deepEqual(previewUnits(config, rows, { kind: 'system', systemId: 1 }).statuses, ['existing', 'new']);
    assert.deepEqual(previewUnits(config, rows, { kind: 'routed' }).statuses, ['existing', 'new']);
    assert.deepEqual(
        previewUnits(config, [{ system: 'Nowhere', id: 7, label: 'x' }], { kind: 'routed' }).statuses,
        ['unrouted'],
    );
});

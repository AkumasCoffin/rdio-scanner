/*
 * *****************************************************************************
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
 * ****************************************************************************
 */

/*
 * Pure merge logic behind the admin Import tool, kept free of Angular
 * imports so the node test runner can exercise it directly (type-only
 * imports erase at runtime).
 *
 * The central semantic: importing into an existing system merges by id —
 * existing entries update in place and keep their order, unknown ids append
 * after the current maximum. That is what makes the export -> rename in a
 * spreadsheet -> re-import round trip land without duplicate-id errors.
 *
 * All lookups go through Maps built once per import: the naive per-row
 * find() over talkgroups/groups/tags was O(rows x entries) and burned
 * millions of comparisons re-importing a large export.
 */

import type { Config, Group, System, Tag, Talkgroup, Unit } from '../admin.service';

// Group/Tag typed here only for the label-resolver signature — talkgroups
// always import/export with their group/tag columns, but targeting BY
// group/tag was removed: it hid the systems list and left users unable to
// import into an existing system (issue #6 feedback).

export interface TalkgroupRow {
    system: string;
    id: number;
    label: string;
    name: string;
    group: string;
    tag: string;
    frequency: string;
    led: string;
    led2: string;
    delay: string;
    alert: string;
}

export interface UnitRow {
    system: string;
    id: number;
    label: string;
}

export interface SystemTarget { kind: 'system'; system: System; }
// 'routed' sends each row to the system named by its CSV `system` column —
// the round-trip complement of the "All systems" export. Without it, a
// multi-system export imported into a single target silently merged every
// system's rows into one.
export type ImportTarget = { kind: 'newSystem' } | { kind: 'routed' } | SystemTarget;

export type UnitsImportTarget = { kind: 'system'; systemId: number | undefined } | { kind: 'routed' };

/**
 * What to do with a CSV row whose id already exists in the target.
 *
 * 'replace' updates the existing entry from the CSV — the round trip an
 * export, edit and re-import expects. 'keep' leaves it exactly as configured
 * and imports only the ids that are new, which is what a broad list (a whole
 * RadioReference dump, say) wants when the entries already set up by hand are
 * the good ones.
 */
export type ImportMode = 'keep' | 'replace';

/** What importing would do to one row. */
export type RowStatus = 'new' | 'existing' | 'unrouted';

/** Row counts by status, for the confirmation dialog. */
export interface ImportPreview {
    statuses: RowStatus[];
    new: number;
    existing: number;
    unrouted: number;
}

/**
 * Works out what importing would do, without doing it.
 *
 * Resolves targets exactly as the import does, so what the review table
 * promises and what the import performs cannot drift apart.
 */
export function previewTalkgroups(config: Config, rows: TalkgroupRow[], target: ImportTarget): ImportPreview {
    const ids = targetIds(
        config,
        target.kind === 'routed',
        target.kind === 'system' ? target.system.id : undefined,
        (system) => system.talkgroups ?? [],
    );

    return summarize(rows.map((row) => rowStatus(ids, target.kind === 'routed' ? row.system : undefined, row.id)));
}

export function previewUnits(config: Config, rows: UnitRow[], target: UnitsImportTarget): ImportPreview {
    const ids = targetIds(
        config,
        target.kind === 'routed',
        target.kind === 'system' ? target.systemId : undefined,
        (system) => system.units ?? [],
    );

    return summarize(rows.map((row) => rowStatus(ids, target.kind === 'routed' ? row.system : undefined, row.id)));
}

/**
 * The ids already present in whatever the rows are aimed at: one set under the
 * empty key for a single target, or one per system label when the rows route
 * themselves. A target that does not exist yet — a new system, or a system id
 * that has since gone — contributes an empty set, so every row reads as new.
 */
function targetIds(
    config: Config,
    routed: boolean,
    systemId: number | undefined,
    entries: (system: System) => { id?: number | null }[],
): Map<string, Set<number>> {
    const systems = config.systems ?? [];

    const collect = (system: System | undefined): Set<number> => {
        const ids = new Set<number>();

        for (const entry of system ? entries(system) : []) {
            if (typeof entry.id === 'number') ids.add(entry.id);
        }

        return ids;
    };

    if (!routed) {
        return new Map([['', collect(systems.find((system) => system.id === systemId))]]);
    }

    const byLabel = new Map<string, Set<number>>();

    for (const system of systems) {
        if (system.label !== undefined) byLabel.set(system.label, collect(system));
    }

    return byLabel;
}

function rowStatus(ids: Map<string, Set<number>>, systemLabel: string | undefined, id: number): RowStatus {
    const known = ids.get(systemLabel ?? '');

    // A routed row naming a system that is not configured goes nowhere. The
    // import already drops those silently; saying so per row is what makes
    // them findable in a list of thousands.
    if (!known) return 'unrouted';

    return known.has(id) ? 'existing' : 'new';
}

function summarize(statuses: RowStatus[]): ImportPreview {
    return {
        statuses,
        new: statuses.filter((status) => status === 'new').length,
        existing: statuses.filter((status) => status === 'existing').length,
        unrouted: statuses.filter((status) => status === 'unrouted').length,
    };
}

// labelIdResolver returns a label -> _id resolver over a groups/tags list,
// creating missing entries with the next free id. The server neither
// validates nor auto-creates groups/tags on config save, and dangling ids
// silently hide talkgroups from the scanner — so creation happens here,
// client-side. Empty labels resolve to undefined so the caller can decide
// (keep-existing on merge, fallback on insert).
function labelIdResolver(list: (Group | Tag)[]): (label: string) => number | undefined {
    const byLabel = new Map<string, number>();
    let nextId = 1;
    for (const entry of list) {
        if (typeof entry._id !== 'number') continue;
        if (entry.label !== undefined && !byLabel.has(entry.label)) {
            byLabel.set(entry.label, entry._id);
        }
        if (entry._id >= nextId) {
            nextId = entry._id + 1;
        }
    }
    return (label: string): number | undefined => {
        if (!label) return undefined;
        let id = byLabel.get(label);
        if (id === undefined) {
            id = nextId++;
            byLabel.set(label, id);
            list.push({ _id: id, label });
        }
        return id;
    };
}

function groupRowsBySystem<T extends { system: string }>(rows: T[]): Map<string, T[]> {
    const bySystem = new Map<string, T[]>();
    for (const row of rows) {
        const systemRows = bySystem.get(row.system);
        if (systemRows) {
            systemRows.push(row);
        } else {
            bySystem.set(row.system, [row]);
        }
    }
    return bySystem;
}

// importTalkgroups applies rows to config for the given target. Returns an
// error message, or null on success.
export function importTalkgroups(
    config: Config,
    rows: TalkgroupRow[],
    target: ImportTarget,
    mode: ImportMode = 'replace',
): string | null {
    config.systems = config.systems ?? [];
    config.groups = config.groups ?? [];
    config.tags = config.tags ?? [];

    const groupId = labelIdResolver(config.groups);
    const tagId = labelIdResolver(config.tags);

    const mergeInto = (system: System, systemRows: TalkgroupRow[]) => {
        system.talkgroups = system.talkgroups ?? [];
        const byId = new Map<number, Talkgroup>();
        let nextOrder = system.talkgroups.length;
        for (const tg of system.talkgroups) {
            if (typeof tg.id === 'number') byId.set(tg.id, tg);
            if (typeof tg.order === 'number' && tg.order > nextOrder) nextOrder = tg.order;
        }

        for (const row of systemRows) {
            // Resolving a label creates the group or tag when it is missing,
            // so skip that for a row keep mode is about to ignore — otherwise
            // importing in keep mode would still grow the group list.
            if (mode === 'keep' && byId.has(row.id)) continue;

            const rowGroupId = groupId(row.group);
            const rowTagId = tagId(row.tag);
            const existing = byId.get(row.id);
            if (existing) {
                // Every field keeps its existing value when the CSV cell is
                // empty — including the label, or a sparse CSV would wipe
                // labels and invalidate previously valid entries.
                if (row.label) existing.label = row.label;
                if (row.name) existing.name = row.name;
                if (rowGroupId !== undefined) existing.groupId = rowGroupId;
                if (rowTagId !== undefined) existing.tagId = rowTagId;
                if (row.frequency) existing.frequency = +row.frequency;
                if (row.led) existing.led = row.led;
                if (row.led2) existing.led2 = row.led2;
                if (row.delay) existing.delay = +row.delay;
                if (row.alert) existing.alert = row.alert;
            } else {
                const talkgroup: Talkgroup = {
                    id: row.id,
                    label: row.label,
                    // name is required by the form; empty descriptions
                    // (common in RadioReference exports) fall back to the
                    // label instead of blocking Save row by row.
                    name: row.name || row.label,
                    groupId: rowGroupId ?? groupId('Unknown'),
                    tagId: rowTagId ?? tagId('Untagged'),
                    frequency: row.frequency ? +row.frequency : null,
                    led: row.led || null,
                    led2: row.led2 || null,
                    delay: row.delay ? +row.delay : 0,
                    alert: row.alert || null,
                    order: ++nextOrder,
                };
                system.talkgroups.push(talkgroup);
                byId.set(row.id, talkgroup);
            }
        }
    };

    if (target.kind === 'system') {
        const system = config.systems.find((s) => s.id === target.system.id);
        if (!system) return 'Target system no longer exists';
        mergeInto(system, rows);
        return null;
    }

    if (target.kind === 'newSystem') {
        // Prefill the next free system id; the label stays blank so Save
        // remains blocked until the user names the system.
        const id = config.systems.reduce((pv, cv) => typeof cv.id === 'number' && cv.id >= pv ? cv.id + 1 : pv, 1);
        const system: System = { id, talkgroups: [] };
        mergeInto(system, rows);
        config.systems.unshift(system);
        return null;
    }

    // Routed target: rows go to the system named by their CSV system
    // column.
    const rowsBySystem = groupRowsBySystem(rows);
    for (const system of config.systems) {
        const systemRows = system.label !== undefined ? rowsBySystem.get(system.label) : undefined;
        if (systemRows?.length) mergeInto(system, systemRows);
    }
    return null;
}

// importUnits merges rows into their target: update labels of existing unit
// ids, append unknown ones with order continuing from the current maximum.
// Returns an error message, or null on success.
export function importUnits(
    config: Config,
    rows: UnitRow[],
    target: UnitsImportTarget,
    mode: ImportMode = 'replace',
): string | null {
    config.systems = config.systems ?? [];

    const mergeInto = (system: System, systemRows: UnitRow[]) => {
        system.units = system.units ?? [];
        const byId = new Map<number, Unit>();
        let nextOrder = system.units.length;
        for (const unit of system.units) {
            if (typeof unit.id === 'number') byId.set(unit.id, unit);
            if (typeof unit.order === 'number' && unit.order > nextOrder) nextOrder = unit.order;
        }

        for (const row of systemRows) {
            const existing = byId.get(row.id);
            if (existing) {
                if (mode === 'keep') continue;

                if (row.label) existing.label = row.label;
            } else {
                const unit: Unit = { id: row.id, label: row.label, order: ++nextOrder };
                system.units.push(unit);
                byId.set(row.id, unit);
            }
        }
    };

    if (target.kind === 'system') {
        const system = config.systems.find((s) => s.id === target.systemId);
        if (!system) return 'Target system no longer exists';
        mergeInto(system, rows);
        return null;
    }

    const rowsBySystem = groupRowsBySystem(rows);
    for (const system of config.systems) {
        const systemRows = system.label !== undefined ? rowsBySystem.get(system.label) : undefined;
        if (systemRows?.length) mergeInto(system, systemRows);
    }
    return null;
}

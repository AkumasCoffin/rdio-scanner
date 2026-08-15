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
 */

import type { Config, Group, System, Tag } from '../admin.service';

export interface TalkgroupRow {
    system: string;
    id: number;
    label: string;
    name: string;
    group: string;
    tag: string;
    frequency: string;
    led: string;
    delay: string;
    alert: string;
}

export interface UnitRow {
    system: string;
    id: number;
    label: string;
}

export interface SystemTarget { kind: 'system'; system: System; }
export interface GroupTarget { kind: 'group'; group: Group; }
export interface TagTarget { kind: 'tag'; tag: Tag; }
export type ImportTarget = { kind: 'newSystem' } | SystemTarget | GroupTarget | TagTarget;

// ensureGroupId resolves a group label to its _id, creating the group when
// missing. The server neither validates nor auto-creates groups/tags on
// config save, and dangling ids silently hide talkgroups from the scanner —
// so creation has to happen here, client-side. Empty labels resolve to
// undefined so the caller can decide (keep-existing on merge, fallback on
// insert).
export function ensureGroupId(config: Config, label: string): number | undefined {
    if (!label) return undefined;
    config.groups = config.groups ?? [];
    let group = config.groups.find((g) => g.label === label);
    if (!group) {
        const id = config.groups.reduce((pv, cv) => typeof cv._id === 'number' && cv._id >= pv ? cv._id + 1 : pv, 1);
        group = { _id: id, label };
        config.groups.push(group);
    }
    return group._id as number;
}

export function ensureTagId(config: Config, label: string): number | undefined {
    if (!label) return undefined;
    config.tags = config.tags ?? [];
    let tag = config.tags.find((t) => t.label === label);
    if (!tag) {
        const id = config.tags.reduce((pv, cv) => typeof cv._id === 'number' && cv._id >= pv ? cv._id + 1 : pv, 1);
        tag = { _id: id, label };
        config.tags.push(tag);
    }
    return tag._id as number;
}

function mergeTalkgroupsInto(
    config: Config,
    system: System,
    rows: TalkgroupRow[],
    forced?: { groupId?: number; tagId?: number },
): void {
    system.talkgroups = system.talkgroups ?? [];
    let nextOrder = system.talkgroups.reduce((pv, cv) => Math.max(pv, cv.order ?? 0), system.talkgroups.length);

    for (const row of rows) {
        const rowGroupId = forced?.groupId ?? ensureGroupId(config, row.group);
        const rowTagId = forced?.tagId ?? ensureTagId(config, row.tag);
        const existing = system.talkgroups.find((t) => t.id === row.id);
        if (existing) {
            existing.label = row.label;
            if (row.name) existing.name = row.name;
            if (rowGroupId !== undefined) existing.groupId = rowGroupId;
            if (rowTagId !== undefined) existing.tagId = rowTagId;
            if (row.frequency) existing.frequency = +row.frequency;
            if (row.led) existing.led = row.led;
            if (row.delay) existing.delay = +row.delay;
            if (row.alert) existing.alert = row.alert;
        } else {
            system.talkgroups.push({
                id: row.id,
                label: row.label,
                // name is required by the form; empty descriptions (common
                // in RadioReference exports) fall back to the label instead
                // of blocking Save row by row.
                name: row.name || row.label,
                groupId: rowGroupId ?? ensureGroupId(config, 'Unknown'),
                tagId: rowTagId ?? ensureTagId(config, 'Untagged'),
                frequency: row.frequency ? +row.frequency : null,
                led: row.led || null,
                delay: row.delay ? +row.delay : 0,
                alert: row.alert || null,
                order: ++nextOrder,
            });
        }
    }
}

// importTalkgroups applies rows to config for the given target. Returns an
// error message, or null on success.
export function importTalkgroups(config: Config, rows: TalkgroupRow[], target: ImportTarget): string | null {
    config.systems = config.systems ?? [];

    if (target.kind === 'system') {
        const system = config.systems.find((s) => s.id === target.system.id);
        if (!system) return 'Target system no longer exists';
        mergeTalkgroupsInto(config, system, rows);
        return null;
    }

    if (target.kind === 'newSystem') {
        // Prefill the next free system id; the label stays blank so Save
        // remains blocked until the user names the system.
        const id = config.systems.reduce((pv, cv) => typeof cv.id === 'number' && cv.id >= pv ? cv.id + 1 : pv, 1);
        const system: System = { id, talkgroups: [] };
        mergeTalkgroupsInto(config, system, rows);
        config.systems.unshift(system);
        return null;
    }

    // Group/tag target: route rows to systems by the CSV's system column,
    // forcing the selected axis; the other axis still honors the CSV.
    const forced = target.kind === 'group'
        ? { groupId: ensureGroupId(config, target.group.label ?? '') }
        : { tagId: ensureTagId(config, target.tag.label ?? '') };
    for (const system of config.systems) {
        const systemRows = rows.filter((r) => r.system === system.label);
        if (systemRows.length) mergeTalkgroupsInto(config, system, systemRows, forced);
    }
    return null;
}

// importUnits merges rows into the system with the given id: update labels
// of existing unit ids, append unknown ones with order continuing from the
// current maximum. Returns an error message, or null on success.
export function importUnits(config: Config, rows: UnitRow[], systemId: number | undefined): string | null {
    const system = (config.systems ?? []).find((s) => s.id === systemId);
    if (!system) return 'Target system no longer exists';

    system.units = system.units ?? [];
    let nextOrder = system.units.reduce((pv, cv) => Math.max(pv, cv.order ?? 0), system.units.length);
    for (const row of rows) {
        const existing = system.units.find((u) => u.id === row.id);
        if (existing) {
            existing.label = row.label;
        } else {
            system.units.push({ id: row.id, label: row.label, order: ++nextOrder });
        }
    }
    return null;
}

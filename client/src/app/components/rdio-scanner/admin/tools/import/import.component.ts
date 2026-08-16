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

import { Component, EventEmitter, OnInit, Output } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import { Config, RdioScannerAdminService, System } from '../../admin.service';
import { decodeCsvBuffer, parseCsv } from '../csv';
import {
    GroupTarget, ImportTarget, SystemTarget, TagTarget, TalkgroupRow, UnitRow,
    importTalkgroups, importUnits,
} from '../import-merge';

type ImportDataType = 'talkgroups' | 'units' | 'config';
type ImportStyle = 'rdio' | 'trunkRecorder' | 'radioReference';

// Rendering thousands of preview rows froze the tab (same class of issue
// as the c47cf21 admin-list freeze); the import itself still processes
// every parsed row.
const PREVIEW_LIMIT = 100;

const TALKGROUP_COLUMNS = ['id', 'label', 'name', 'group', 'tag', 'action'];
const TALKGROUP_COLUMNS_WITH_SYSTEM = ['system', ...TALKGROUP_COLUMNS];
const UNIT_COLUMNS = ['id', 'label', 'action'];
const UNIT_COLUMNS_WITH_SYSTEM = ['system', ...UNIT_COLUMNS];

@Component({
    selector: 'rdio-scanner-admin-import',
    styleUrls: ['./import.component.scss'],
    templateUrl: './import.component.html',
})
export class RdioScannerAdminImportComponent implements OnInit {
    @Output() config = new EventEmitter<Config>();

    baseConfig: Config = {};

    dataType: ImportDataType = 'talkgroups';

    style: ImportStyle = 'trunkRecorder';

    // Positional presets, unchanged from the original importer:
    // column indexes for [id, label, name, tag, group].
    private fields: Record<'trunkRecorder' | 'radioReference', number[]> = {
        trunkRecorder: [0, 3, 4, 5, 6],
        radioReference: [0, 2, 4, 5, 6],
    };

    private headerMap: Record<string, number> = {};

    private hasHeader = false;

    rawRows: string[][] = [];

    talkgroupRows: TalkgroupRow[] = [];

    talkgroupPreview: TalkgroupRow[] = [];

    unitRows: UnitRow[] = [];

    unitPreview: UnitRow[] = [];

    // Distinct non-empty system labels in the parsed rows — drives the
    // multi-system warning without re-scanning rows every change detection.
    private distinctSystems = 0;

    newSystemTarget: ImportTarget = { kind: 'newSystem' };

    routedTarget: ImportTarget = { kind: 'routed' };

    target: ImportTarget = this.newSystemTarget;

    systemTargets: SystemTarget[] = [];

    groupTargets: GroupTarget[] = [];

    tagTargets: TagTarget[] = [];

    unknownSystems: string[] = [];

    previewLimit = PREVIEW_LIMIT;

    trackByIndex = (index: number): number => index;

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) { }

    async ngOnInit(): Promise<void> {
        await this.refresh();
    }

    async refresh(): Promise<void> {
        this.baseConfig = await this.adminService.getConfig();

        this.systemTargets = (this.baseConfig.systems ?? []).map((system) => ({ kind: 'system' as const, system }));
        // Group/tag targets follow the same sort options as the rest of
        // the UI (mutually exclusive, so at most one optgroup shows).
        this.groupTargets = this.baseConfig.options?.sortByGroups
            ? (this.baseConfig.groups ?? []).map((group) => ({ kind: 'group' as const, group }))
            : [];
        this.tagTargets = this.baseConfig.options?.sortByTags
            ? (this.baseConfig.tags ?? []).map((tag) => ({ kind: 'tag' as const, tag }))
            : [];

        this.reconcileTarget();
    }

    get talkgroupColumns(): string[] {
        return this.style === 'rdio' ? TALKGROUP_COLUMNS_WITH_SYSTEM : TALKGROUP_COLUMNS;
    }

    get unitColumns(): string[] {
        return this.csvHasSystemColumn ? UNIT_COLUMNS_WITH_SYSTEM : UNIT_COLUMNS;
    }

    get rowCount(): number {
        return this.dataType === 'units' ? this.unitRows.length : this.talkgroupRows.length;
    }

    get csvHasSystemColumn(): boolean {
        return this.hasHeader && 'system' in this.headerMap;
    }

    // Routed / group / tag targets need the CSV to say which system each
    // row belongs to — only the exported Rdio Scanner format carries that
    // column.
    get routingError(): boolean {
        if (!(this.target.kind === 'routed' || this.target.kind === 'group' || this.target.kind === 'tag')) {
            return false;
        }
        if (this.dataType === 'talkgroups' && this.style !== 'rdio') {
            return true;
        }
        return !this.csvHasSystemColumn;
    }

    // A multi-system CSV pointed at a single target imports every row into
    // that one target — legal, but easy to do by accident with an
    // "All systems" export, so it gets a warning steering toward routed.
    get multiSystemWarning(): boolean {
        return this.distinctSystems > 1
            && (this.target.kind === 'system' || this.target.kind === 'newSystem');
    }

    get canImport(): boolean {
        if (!this.rowCount || this.routingError || this.unknownSystems.length > 0) {
            return false;
        }
        if (this.dataType === 'units') {
            return this.target.kind === 'system' || this.target.kind === 'routed';
        }
        return true;
    }

    systemLabel(system: System): string {
        return system.label || `System ${system.id}`;
    }

    onDataTypeChange(): void {
        this.reset();
        this.target = this.dataType === 'units'
            ? (this.systemTargets[0] ?? this.routedTarget)
            : this.newSystemTarget;
    }

    reset(): void {
        this.rawRows = [];
        this.talkgroupRows = [];
        this.talkgroupPreview = [];
        this.unitRows = [];
        this.unitPreview = [];
        this.unknownSystems = [];
        this.distinctSystems = 0;
    }

    // reconcileTarget re-points the selected target at the freshly fetched
    // lists — resetting to the default here silently discarded a target the
    // user had already chosen whenever the panel reopened or a file was
    // read.
    private reconcileTarget(): void {
        const target = this.target;
        if (target.kind === 'system') {
            const match = this.systemTargets.find((t) => t.system.id === target.system.id);
            this.target = match ?? this.defaultTarget();
        } else if (target.kind === 'group') {
            const match = this.groupTargets.find((t) => t.group._id === target.group._id);
            this.target = match ?? this.defaultTarget();
        } else if (target.kind === 'tag') {
            const match = this.tagTargets.find((t) => t.tag._id === target.tag._id);
            this.target = match ?? this.defaultTarget();
        } else if (this.dataType === 'units' && target.kind === 'newSystem') {
            this.target = this.defaultTarget();
        }
    }

    private defaultTarget(): ImportTarget {
        return this.dataType === 'units'
            ? (this.systemTargets[0] ?? this.routedTarget)
            : this.newSystemTarget;
    }

    async read(event: Event): Promise<void> {
        const target = (event.target as HTMLInputElement & EventTarget);

        const file = target.files?.item(0);

        if (!(file instanceof File)) return;

        if (this.dataType === 'config') {
            this.readConfig(target, file);
            return;
        }

        // Fresh config so the target lists and system-label routing don't
        // work against a stale snapshot from when the panel first rendered.
        await this.refresh();

        const reader = new FileReader();

        reader.onloadend = () => {
            target.value = '';

            if (!(reader.result instanceof ArrayBuffer)) return;

            this.rawRows = parseCsv(decodeCsvBuffer(reader.result));
            this.detectHeader();
            this.remap();
        };

        reader.readAsArrayBuffer(file);
    }

    private detectHeader(): void {
        const first = (this.rawRows[0] ?? []).map((c) => c.trim().toLowerCase());
        this.hasHeader = first.includes('id') && first.includes('label');
        this.headerMap = {};

        if (this.hasHeader) {
            first.forEach((name, idx) => {
                if (!(name in this.headerMap)) this.headerMap[name] = idx;
            });
            if (this.dataType === 'talkgroups') this.style = 'rdio';
        } else if (this.dataType === 'talkgroups' && this.style === 'rdio') {
            this.style = 'trunkRecorder';
        }
    }

    remap(): void {
        if (this.dataType === 'talkgroups') {
            this.remapTalkgroups();
        } else if (this.dataType === 'units') {
            this.remapUnits();
        }
        this.updateUnknownSystems();
    }

    private headerCell(row: string[], name: string): string {
        const idx = this.headerMap[name];
        return idx === undefined ? '' : (row[idx] ?? '').trim();
    }

    private remapTalkgroups(): void {
        interface RawTalkgroup extends Omit<TalkgroupRow, 'id'> { idStr: string; }
        let rows: RawTalkgroup[];

        if (this.style === 'rdio') {
            if (!this.hasHeader) {
                this.setTalkgroupRows([]);
                return;
            }
            rows = this.rawRows.slice(1).map((r) => ({
                system: this.headerCell(r, 'system'),
                idStr: this.headerCell(r, 'id'),
                label: this.headerCell(r, 'label'),
                name: this.headerCell(r, 'name'),
                group: this.headerCell(r, 'group'),
                tag: this.headerCell(r, 'tag'),
                frequency: this.headerCell(r, 'frequency'),
                led: this.headerCell(r, 'led'),
                delay: this.headerCell(r, 'delay'),
                alert: this.headerCell(r, 'alert'),
            }));
        } else {
            const f = this.fields[this.style];
            rows = this.rawRows.map((r) => ({
                system: '',
                idStr: (r[f[0]] ?? '').trim(),
                label: (r[f[1]] ?? '').trim(),
                name: (r[f[2]] ?? '').trim(),
                tag: (r[f[3]] ?? '').trim(),
                group: (r[f[4]] ?? '').trim(),
                frequency: '',
                led: '',
                delay: '',
                alert: '',
            }));
        }

        // In the exported format the same talkgroup id can exist in two
        // systems, so dedupe per (system, id); the positional presets are
        // single-system, plain id.
        const seen = new Set<string>();
        this.setTalkgroupRows(rows
            .filter((r) => /^[0-9]+$/.test(r.idStr))
            .filter((r) => {
                const key = this.style === 'rdio' ? `${r.system} ${r.idStr}` : r.idStr;
                if (seen.has(key)) return false;
                seen.add(key);
                return true;
            })
            .map(({ idStr, ...rest }) => ({ ...rest, id: +idStr })));
    }

    private remapUnits(): void {
        let rows: { system: string; idStr: string; label: string }[];

        if (this.hasHeader) {
            rows = this.rawRows.slice(1).map((r) => ({
                system: this.headerCell(r, 'system'),
                idStr: this.headerCell(r, 'id'),
                label: this.headerCell(r, 'label'),
            }));
        } else {
            rows = this.rawRows.map((r) => ({
                system: '',
                idStr: (r[0] ?? '').trim(),
                label: (r[1] ?? '').trim(),
            }));
        }

        // Same (system, id) dedupe as talkgroups — a plain id key dropped
        // legitimate same-id units from different systems in an
        // "All systems" export.
        const seen = new Set<string>();
        this.setUnitRows(rows
            .filter((r) => /^[0-9]+$/.test(r.idStr))
            .filter((r) => {
                const key = this.hasHeader ? `${r.system} ${r.idStr}` : r.idStr;
                if (seen.has(key)) return false;
                seen.add(key);
                return true;
            })
            .map((r) => ({ system: r.system, id: +r.idStr, label: r.label })));
    }

    private setTalkgroupRows(rows: TalkgroupRow[]): void {
        this.talkgroupRows = rows;
        this.talkgroupPreview = rows.slice(0, PREVIEW_LIMIT);
        this.distinctSystems = new Set(rows.map((r) => r.system).filter((s) => s)).size;
    }

    private setUnitRows(rows: UnitRow[]): void {
        this.unitRows = rows;
        this.unitPreview = rows.slice(0, PREVIEW_LIMIT);
        this.distinctSystems = new Set(rows.map((r) => r.system).filter((s) => s)).size;
    }

    updateUnknownSystems(): void {
        if (!(this.target.kind === 'routed' || this.target.kind === 'group' || this.target.kind === 'tag')) {
            this.unknownSystems = [];
            return;
        }
        const known = new Set((this.baseConfig.systems ?? []).map((s) => s.label));
        const rows: { system: string }[] = this.dataType === 'units' ? this.unitRows : this.talkgroupRows;
        this.unknownSystems = [...new Set(
            rows.filter((r) => !known.has(r.system)).map((r) => r.system || '(empty)'),
        )];
    }

    removeTalkgroupRow(index: number): void {
        // Preview is the first PREVIEW_LIMIT rows, so a preview index is
        // the same index in the full list.
        this.talkgroupRows.splice(index, 1);
        this.setTalkgroupRows(this.talkgroupRows);
        this.updateUnknownSystems();
    }

    removeUnitRow(index: number): void {
        this.unitRows.splice(index, 1);
        this.setUnitRows(this.unitRows);
        this.updateUnknownSystems();
    }

    async import(): Promise<void> {
        // Work on a fresh config: the emit downstream rebuilds the whole
        // form from it, and a stale snapshot would revert other sections.
        const config = await this.adminService.getConfig();

        const error = this.dataType === 'talkgroups'
            ? importTalkgroups(config, this.talkgroupRows, this.target)
            : importUnits(config, this.unitRows, this.target.kind === 'routed'
                ? { kind: 'routed' }
                : { kind: 'system', systemId: this.target.kind === 'system' ? this.target.system.id : undefined });

        if (error) {
            this.matSnackBar.open(error, '', { duration: 5000 });
            return;
        }

        this.reset();

        this.config.emit(config);
    }

    // The whole-config JSON import, moved verbatim from the retired
    // import/export config tool — the binary-string decode must stay so
    // files exported by older versions keep importing.
    private readConfig(target: HTMLInputElement, file: File): void {
        const reader = new FileReader();

        reader.onloadend = () => {
            target.value = '';

            try {
                const res = decodeURIComponent(Array.prototype.map.call(reader.result, (c) => {
                    return '%' + ('00' + (c as string).charCodeAt(0).toString(16)).slice(-2)
                }).join(''));

                this.config.emit(JSON.parse(res));

            } catch (error) {
                this.matSnackBar.open(error as string, '', { duration: 5000 });
            }
        };

        reader.readAsBinaryString(file);
    }
}

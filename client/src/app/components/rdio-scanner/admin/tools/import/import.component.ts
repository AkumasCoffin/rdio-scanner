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
import { parseCsv } from '../csv';
import {
    GroupTarget, ImportTarget, SystemTarget, TagTarget, TalkgroupRow, UnitRow,
    importTalkgroups, importUnits,
} from '../import-merge';

type ImportDataType = 'talkgroups' | 'units' | 'config';
type ImportStyle = 'rdio' | 'trunkRecorder' | 'radioReference';

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

    unitRows: UnitRow[] = [];

    newSystemTarget: ImportTarget = { kind: 'newSystem' };

    target: ImportTarget = this.newSystemTarget;

    systemTargets: SystemTarget[] = [];

    groupTargets: GroupTarget[] = [];

    tagTargets: TagTarget[] = [];

    unknownSystems: string[] = [];

    unitColumns = ['id', 'label', 'action'];

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

        this.resetTarget();
    }

    get talkgroupColumns(): string[] {
        return this.style === 'rdio'
            ? ['system', 'id', 'label', 'name', 'group', 'tag', 'action']
            : ['id', 'label', 'name', 'group', 'tag', 'action'];
    }

    get rowCount(): number {
        return this.dataType === 'units' ? this.unitRows.length : this.talkgroupRows.length;
    }

    // Group/tag targets need the CSV to say which system each row belongs
    // to — only the exported Rdio Scanner format carries that column.
    get routingError(): boolean {
        return this.dataType === 'talkgroups'
            && (this.target.kind === 'group' || this.target.kind === 'tag')
            && !(this.style === 'rdio' && this.hasHeader && 'system' in this.headerMap);
    }

    get canImport(): boolean {
        if (this.dataType === 'units') {
            return this.unitRows.length > 0 && this.target.kind === 'system';
        }
        return this.talkgroupRows.length > 0 && !this.routingError && this.unknownSystems.length === 0;
    }

    systemLabel(system: System): string {
        return system.label || `System ${system.id}`;
    }

    onDataTypeChange(): void {
        this.reset();
        this.resetTarget();
    }

    reset(): void {
        this.rawRows = [];
        this.talkgroupRows = [];
        this.unitRows = [];
        this.unknownSystems = [];
    }

    private resetTarget(): void {
        this.target = this.dataType === 'units'
            ? (this.systemTargets[0] ?? this.newSystemTarget)
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

            if (typeof reader.result !== 'string') return;

            this.rawRows = parseCsv(reader.result);
            this.detectHeader();
            this.remap();
        };

        reader.readAsText(file);
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

    private remapTalkgroups(): void {
        interface RawTalkgroup extends Omit<TalkgroupRow, 'id'> { idStr: string; }
        let rows: RawTalkgroup[];

        if (this.style === 'rdio') {
            if (!this.hasHeader) {
                this.talkgroupRows = [];
                return;
            }
            const h = this.headerMap;
            const cell = (r: string[], name: string) => {
                const i = h[name];
                return i === undefined ? '' : (r[i] ?? '').trim();
            };
            rows = this.rawRows.slice(1).map((r) => ({
                system: cell(r, 'system'),
                idStr: cell(r, 'id'),
                label: cell(r, 'label'),
                name: cell(r, 'name'),
                group: cell(r, 'group'),
                tag: cell(r, 'tag'),
                frequency: cell(r, 'frequency'),
                led: cell(r, 'led'),
                delay: cell(r, 'delay'),
                alert: cell(r, 'alert'),
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
        this.talkgroupRows = rows
            .filter((r) => /^[0-9]+$/.test(r.idStr))
            .filter((r) => {
                const key = this.style === 'rdio' ? `${r.system} ${r.idStr}` : r.idStr;
                if (seen.has(key)) return false;
                seen.add(key);
                return true;
            })
            .map(({ idStr, ...rest }) => ({ ...rest, id: +idStr }));
    }

    private remapUnits(): void {
        let rows: { system: string; idStr: string; label: string }[];

        if (this.hasHeader) {
            const h = this.headerMap;
            const cell = (r: string[], name: string) => {
                const i = h[name];
                return i === undefined ? '' : (r[i] ?? '').trim();
            };
            rows = this.rawRows.slice(1).map((r) => ({
                system: cell(r, 'system'),
                idStr: cell(r, 'id'),
                label: cell(r, 'label'),
            }));
        } else {
            rows = this.rawRows.map((r) => ({
                system: '',
                idStr: (r[0] ?? '').trim(),
                label: (r[1] ?? '').trim(),
            }));
        }

        const seen = new Set<string>();
        this.unitRows = rows
            .filter((r) => /^[0-9]+$/.test(r.idStr))
            .filter((r) => {
                if (seen.has(r.idStr)) return false;
                seen.add(r.idStr);
                return true;
            })
            .map((r) => ({ system: r.system, id: +r.idStr, label: r.label }));
    }

    updateUnknownSystems(): void {
        if (this.dataType === 'talkgroups' && (this.target.kind === 'group' || this.target.kind === 'tag')) {
            const known = new Set((this.baseConfig.systems ?? []).map((s) => s.label));
            this.unknownSystems = [...new Set(
                this.talkgroupRows.filter((r) => !known.has(r.system)).map((r) => r.system || '(empty)'),
            )];
        } else {
            this.unknownSystems = [];
        }
    }

    removeTalkgroupRow(index: number): void {
        this.talkgroupRows.splice(index, 1);
        this.updateUnknownSystems();
    }

    async import(): Promise<void> {
        // Work on a fresh config: the emit downstream rebuilds the whole
        // form from it, and a stale snapshot would revert other sections.
        const config = await this.adminService.getConfig();

        const error = this.dataType === 'talkgroups'
            ? importTalkgroups(config, this.talkgroupRows, this.target)
            : importUnits(config, this.unitRows, this.target.kind === 'system' ? this.target.system.id : undefined);

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

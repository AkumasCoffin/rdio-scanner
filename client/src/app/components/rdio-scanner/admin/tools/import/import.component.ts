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
import { MatDialog } from '@angular/material/dialog';
import { MatSnackBar } from '@angular/material/snack-bar';
import { firstValueFrom } from 'rxjs';
import { Config, RdioScannerAdminService, System } from '../../admin.service';
import { decodeCsvBuffer, parseCsv } from '../csv';
import {
    ImportMode, ImportPreview, ImportTarget, PatchRow, RowStatus, SystemTarget, TalkgroupRow, UnitRow,
    UnitsImportTarget, importPatches, importTalkgroups, importUnits, parseTalkgroupList, previewPatches,
    previewTalkgroups, previewUnits,
} from '../import-merge';
import { RdioScannerAdminImportMergeDialogComponent } from './merge-dialog.component';

type ImportDataType = 'talkgroups' | 'units' | 'patches' | 'config';
type ImportStyle = 'rdio' | 'trunkRecorder' | 'radioReference';

/** What the review list shows for one parsed row. */
export interface PreviewEntry<T> {
    row: T;
    status: RowStatus;
    /** Position in the full row list, so removing a row hits the right one. */
    index: number;
}

/** Which rows the review list shows. */
export type StatusFilter = 'all' | RowStatus;

const EMPTY_PREVIEW: ImportPreview = { statuses: [], new: 0, existing: 0, unrouted: 0 };

// Row height in the virtualised review list, and the height it grows to
// before scrolling instead. Must match .preview-row in the stylesheet: the
// viewport positions rows from this number, not from what it measures.
const ROW_HEIGHT = 40;
const MAX_VIEWPORT_HEIGHT = 480;

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

    patchRows: PatchRow[] = [];

    // What the review list renders: every row that passes the status filter,
    // paired with what importing would do to it. The list is virtualised, so
    // this is the whole CSV rather than a first-hundred sample — a preview
    // that stops at 100 rows cannot answer "what would this replace?" for a
    // file with thousands.
    talkgroupEntries: PreviewEntry<TalkgroupRow>[] = [];

    unitEntries: PreviewEntry<UnitRow>[] = [];

    patchEntries: PreviewEntry<PatchRow>[] = [];

    counts: ImportPreview = EMPTY_PREVIEW;

    statusFilter: StatusFilter = 'all';

    // Distinct non-empty system labels in the parsed rows — drives the
    // multi-system warning without re-scanning rows every change detection.
    private distinctSystems = 0;

    newSystemTarget: ImportTarget = { kind: 'newSystem' };

    routedTarget: ImportTarget = { kind: 'routed' };

    target: ImportTarget = this.newSystemTarget;

    systemTargets: SystemTarget[] = [];

    unknownSystems: string[] = [];

    // Keyed on the row's real position, so scrolling a virtualised list does
    // not rebuild rows that only moved viewport slot.
    trackByEntry = (_: number, entry: PreviewEntry<unknown>): number => entry.index;

    constructor(
        private adminService: RdioScannerAdminService,
        private matDialog: MatDialog,
        private matSnackBar: MatSnackBar,
    ) { }

    async ngOnInit(): Promise<void> {
        await this.refresh();
    }

    async refresh(): Promise<void> {
        this.baseConfig = await this.adminService.getConfig();

        // Targets are systems only — talkgroup group/tag assignments come
        // from the CSV columns. Targeting by group/tag was removed: it hid
        // the systems list and left users unable to import into an
        // existing system (issue #6 feedback).
        this.systemTargets = (this.baseConfig.systems ?? []).map((system) => ({ kind: 'system' as const, system }));

        this.reconcileTarget();
    }

    get rowCount(): number {
        if (this.dataType === 'units') return this.unitRows.length;
        if (this.dataType === 'patches') return this.patchRows.length;

        return this.talkgroupRows.length;
    }

    get csvHasSystemColumn(): boolean {
        return this.hasHeader && 'system' in this.headerMap;
    }

    /** True when the review list has a System column to show. */
    get showSystemColumn(): boolean {
        return this.dataType === 'talkgroups' ? this.style === 'rdio' : this.csvHasSystemColumn;
    }

    /** How many rows the current status filter is showing. */
    get shownCount(): number {
        if (this.dataType === 'units') return this.unitEntries.length;
        if (this.dataType === 'patches') return this.patchEntries.length;

        return this.talkgroupEntries.length;
    }

    /**
     * The review list grows with its content up to a limit, then scrolls.
     * A viewport fixed at full height leaves a short CSV floating in empty
     * space; one sized purely to content puts the import button below the
     * fold for a long one.
     */
    get viewportHeight(): number {
        return Math.min(MAX_VIEWPORT_HEIGHT, Math.max(ROW_HEIGHT, this.shownCount * ROW_HEIGHT));
    }

    statusLabel(status: RowStatus): string {
        if (status === 'existing') return 'Replaces';

        if (status !== 'unrouted') return 'New';

        // For a patch the same status covers a second reason: the system is
        // there but does not carry enough of the talkgroups named. Both mean
        // the row is left out, and the caption under the list says which.
        return this.dataType === 'patches' ? 'Skipped' : 'No system';
    }

    // The routed target needs the CSV to say which system each row belongs
    // to — only the exported Rdio Scanner format carries that column.
    get routingError(): boolean {
        if (this.target.kind !== 'routed') {
            return false;
        }
        if (this.dataType === 'talkgroups' && this.style !== 'rdio') {
            return true;
        }
        return !this.csvHasSystemColumn;
    }

    /** Rows a patch import will skip, and why, for the caption under the list. */
    get unimportablePatches(): number {
        return this.dataType === 'patches' ? this.counts.unrouted : 0;
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
        if (this.needsExistingSystem) {
            return this.target.kind === 'system' || this.target.kind === 'routed';
        }
        return true;
    }

    systemLabel(system: System): string {
        return system.label || `System ${system.id}`;
    }

    onDataTypeChange(): void {
        this.reset();
        this.target = this.defaultTarget();
    }

    reset(): void {
        this.rawRows = [];
        this.talkgroupRows = [];
        this.talkgroupEntries = [];
        this.unitRows = [];
        this.unitEntries = [];
        this.patchRows = [];
        this.patchEntries = [];
        this.unknownSystems = [];
        this.distinctSystems = 0;
        this.counts = EMPTY_PREVIEW;
        this.statusFilter = 'all';
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
        } else if (this.needsExistingSystem && target.kind === 'newSystem') {
            this.target = this.defaultTarget();
        }
    }

    // A new system has no talkgroups, so neither units nor patches can go
    // into one — both of them name things a system already has.
    private get needsExistingSystem(): boolean {
        return this.dataType === 'units' || this.dataType === 'patches';
    }

    private defaultTarget(): ImportTarget {
        return this.needsExistingSystem
            ? (this.routedTarget ?? this.systemTargets[0])
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

        // A patch row has no id of its own — it is named, and its members are
        // a column — so the header that identifies one is different.
        this.hasHeader = this.dataType === 'patches'
            ? first.includes('label') && first.includes('talkgroups')
            : first.includes('id') && first.includes('label');
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
        } else if (this.dataType === 'patches') {
            this.remapPatches();
        }
        this.updateUnknownSystems();
    }

    /**
     * Patches always carry a header — the members live in a named column and
     * there is no positional convention from another tool to fall back on.
     */
    private remapPatches(): void {
        if (!this.hasHeader) {
            this.setPatchRows([]);
            return;
        }

        const rows = this.rawRows.slice(1)
            .map((r) => ({
                system: this.headerCell(r, 'system'),
                label: this.headerCell(r, 'label'),
                talkgroups: parseTalkgroupList(this.headerCell(r, 'talkgroups')),
                delay: this.headerCell(r, 'delay'),
                disabled: this.headerCell(r, 'disabled'),
            }))
            .filter((r) => r.label !== '');

        this.setPatchRows(rows);
    }

    private setPatchRows(rows: PatchRow[]): void {
        this.patchRows = rows;
        this.distinctSystems = new Set(rows.map((r) => r.system).filter((s) => s)).size;
    }

    removePatchRow(index: number): void {
        this.patchRows.splice(index, 1);
        this.setPatchRows(this.patchRows);
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
                led2: this.headerCell(r, 'led2'),
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
                led2: '',
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
        this.distinctSystems = new Set(rows.map((r) => r.system).filter((s) => s)).size;
    }

    private setUnitRows(rows: UnitRow[]): void {
        this.unitRows = rows;
        this.distinctSystems = new Set(rows.map((r) => r.system).filter((s) => s)).size;
    }

    // Also the single place the preview is rebuilt: it is called after every
    // parse and on every target change, which is exactly when what a row
    // would do can change.
    updateUnknownSystems(): void {
        if (this.target.kind === 'routed') {
            const known = new Set((this.baseConfig.systems ?? []).map((s) => s.label));
            const rows: { system: string }[] = this.dataType === 'units' ? this.unitRows
                : this.dataType === 'patches' ? this.patchRows : this.talkgroupRows;
            this.unknownSystems = [...new Set(
                rows.filter((r) => !known.has(r.system)).map((r) => r.system || '(empty)'),
            )];
        } else {
            this.unknownSystems = [];
        }

        this.refreshPreview();
    }

    setStatusFilter(filter: StatusFilter): void {
        this.statusFilter = filter;
        this.refreshPreview();
    }

    private refreshPreview(): void {
        if (this.dataType === 'patches') {
            this.counts = previewPatches(this.baseConfig, this.patchRows, this.unitsTarget());
            this.dropEmptyFilter();
            this.patchEntries = this.buildEntries(this.patchRows, this.counts.statuses);
            this.talkgroupEntries = [];
            this.unitEntries = [];

        } else if (this.dataType === 'units') {
            this.counts = previewUnits(this.baseConfig, this.unitRows, this.unitsTarget());
            this.dropEmptyFilter();
            this.unitEntries = this.buildEntries(this.unitRows, this.counts.statuses);
            this.talkgroupEntries = [];
            this.patchEntries = [];
        } else if (this.dataType === 'talkgroups') {
            this.counts = previewTalkgroups(this.baseConfig, this.talkgroupRows, this.target);
            this.dropEmptyFilter();
            this.talkgroupEntries = this.buildEntries(this.talkgroupRows, this.counts.statuses);
            this.unitEntries = [];
            this.patchEntries = [];
        } else {
            this.counts = EMPTY_PREVIEW;
        }
    }

    /**
     * Changing the target can empty the filtered category — most obviously
     * the unrouted one, whose chip only exists while it has rows. Leaving the
     * filter on it shows an empty list next to a set of chips that no longer
     * offers the one it is stuck on, so the filter falls back to showing
     * everything.
     */
    private dropEmptyFilter(): void {
        const remaining = this.statusFilter === 'new' ? this.counts.new
            : this.statusFilter === 'existing' ? this.counts.existing
                : this.statusFilter === 'unrouted' ? this.counts.unrouted : 1;

        if (!remaining) {
            this.statusFilter = 'all';
        }
    }

    private buildEntries<T>(rows: T[], statuses: RowStatus[]): PreviewEntry<T>[] {
        const entries: PreviewEntry<T>[] = [];

        rows.forEach((row, index) => {
            const status = statuses[index] ?? 'new';

            if (this.statusFilter === 'all' || status === this.statusFilter) {
                entries.push({ row, status, index });
            }
        });

        return entries;
    }

    /** The units target the current selection resolves to. */
    private unitsTarget(): UnitsImportTarget {
        return this.target.kind === 'routed'
            ? { kind: 'routed' }
            : { kind: 'system', systemId: this.target.kind === 'system' ? this.target.system.id : undefined };
    }

    removeTalkgroupRow(index: number): void {
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
        const mode = await this.askMode();

        if (!mode) {
            return;
        }

        // Work on a fresh config: the emit downstream rebuilds the whole
        // form from it, and a stale snapshot would revert other sections.
        const config = await this.adminService.getConfig();

        const error = this.dataType === 'talkgroups'
            ? importTalkgroups(config, this.talkgroupRows, this.target, mode)
            : this.dataType === 'patches'
                ? importPatches(config, this.patchRows, this.unitsTarget(), mode)
                : importUnits(config, this.unitRows, this.unitsTarget(), mode);

        if (error) {
            this.matSnackBar.open(error, '', { duration: 5000 });
            return;
        }

        this.reset();

        this.config.emit(config);
    }

    /**
     * Asks what to do about rows that land on ids the target already has.
     *
     * Returns undefined when the user backs out. Nothing to overwrite means
     * nothing to ask, so a clean import goes straight through.
     */
    private async askMode(): Promise<ImportMode | undefined> {
        if (!this.counts.existing) {
            return 'replace';
        }

        const answer = await firstValueFrom(
            this.matDialog.open(RdioScannerAdminImportMergeDialogComponent, {
                data: {
                    noun: this.dataType === 'units' ? 'units'
                        : this.dataType === 'patches' ? 'patches' : 'talkgroups',
                    existing: this.counts.existing,
                    new: this.counts.new,
                },
                width: '34rem',
                maxWidth: '95vw',
            }).afterClosed(),
        );

        return answer ? (answer.mode as ImportMode) : undefined;
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

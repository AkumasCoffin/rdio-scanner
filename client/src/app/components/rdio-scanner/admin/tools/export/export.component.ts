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

import { DOCUMENT } from '@angular/common';
import { Component, Inject, OnInit } from '@angular/core';
import { Config, RdioScannerAdminService, System } from '../../admin.service';
import { downloadFile, slugify, toCsv } from '../csv';

type ExportDataType = 'talkgroups' | 'units' | 'config';

interface SystemScope { kind: 'system'; system: System; }
interface GroupScope { kind: 'group'; id: number; label: string; }
interface TagScope { kind: 'tag'; id: number; label: string; }
type ExportScope = { kind: 'all' } | SystemScope | GroupScope | TagScope;

@Component({
    selector: 'rdio-scanner-admin-export',
    styleUrls: ['./export.component.scss'],
    templateUrl: './export.component.html',
})
export class RdioScannerAdminExportComponent implements OnInit {
    baseConfig: Config = {};

    dataType: ExportDataType = 'talkgroups';

    allScope: ExportScope = { kind: 'all' };

    scope: ExportScope = this.allScope;

    systemScopes: SystemScope[] = [];

    groupScopes: GroupScope[] = [];

    tagScopes: TagScope[] = [];

    constructor(
        private adminService: RdioScannerAdminService,
        @Inject(DOCUMENT) private document: Document,
    ) { }

    async ngOnInit(): Promise<void> {
        await this.refresh();
    }

    async refresh(): Promise<void> {
        this.baseConfig = await this.adminService.getConfig();

        this.systemScopes = (this.baseConfig.systems ?? []).map((system) => ({ kind: 'system' as const, system }));
        // Group/tag scopes only show when the corresponding sort option is
        // on — the same lens the rest of the UI uses. The options are
        // mutually exclusive, so at most one extra optgroup appears.
        this.groupScopes = this.baseConfig.options?.sortByGroups
            ? (this.baseConfig.groups ?? [])
                .filter((g) => typeof g._id === 'number')
                .map((g) => ({ kind: 'group' as const, id: g._id as number, label: g.label ?? '' }))
            : [];
        this.tagScopes = this.baseConfig.options?.sortByTags
            ? (this.baseConfig.tags ?? [])
                .filter((t) => typeof t._id === 'number')
                .map((t) => ({ kind: 'tag' as const, id: t._id as number, label: t.label ?? '' }))
            : [];

        this.scope = this.allScope;
    }

    get hasSystems(): boolean {
        return this.systemScopes.length > 0;
    }

    get canExport(): boolean {
        return this.dataType === 'config' || this.hasSystems;
    }

    onDataTypeChange(): void {
        // Units have no group/tag axis; drop such a scope when switching.
        if (this.dataType !== 'talkgroups' && (this.scope.kind === 'group' || this.scope.kind === 'tag')) {
            this.scope = this.allScope;
        }
    }

    systemLabel(system: System): string {
        return system.label || `System ${system.id}`;
    }

    async export(): Promise<void> {
        if (this.dataType === 'config') {
            await this.exportConfig();
            return;
        }

        // Re-fetch so the CSV reflects the saved config as of right now,
        // not whenever this panel was first rendered.
        const config = await this.adminService.getConfig();
        const systems = config.systems ?? [];
        const groups = config.groups ?? [];
        const tags = config.tags ?? [];

        // Maps, not per-row find() — a large export otherwise rescans the
        // whole groups/tags lists for every row.
        const groupLabels = new Map(groups.map((g) => [g._id, g.label ?? '']));
        const tagLabels = new Map(tags.map((t) => [t._id, t.label ?? '']));
        const groupLabel = (id?: number) => (id !== undefined ? groupLabels.get(id) : undefined) ?? '';
        const tagLabel = (id?: number) => (id !== undefined ? tagLabels.get(id) : undefined) ?? '';

        const scope = this.scope;
        const scopedSystems = scope.kind === 'system'
            ? systems.filter((s) => s.id === scope.system.id)
            : systems;

        const rows: (string | number | null | undefined)[][] = [];
        let slug: string;

        if (this.dataType === 'talkgroups') {
            rows.push(['system', 'id', 'label', 'name', 'group', 'tag', 'frequency', 'led', 'delay', 'alert']);
            for (const system of scopedSystems) {
                const talkgroups = [...(system.talkgroups ?? [])].sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
                for (const tg of talkgroups) {
                    if (scope.kind === 'group' && tg.groupId !== scope.id) continue;
                    if (scope.kind === 'tag' && tg.tagId !== scope.id) continue;
                    rows.push([
                        system.label, tg.id, tg.label, tg.name,
                        groupLabel(tg.groupId), tagLabel(tg.tagId),
                        tg.frequency, tg.led, tg.delay, tg.alert,
                    ]);
                }
            }
            slug = this.scopeSlug();
            downloadFile(this.document, `rdio-scanner-talkgroups-${slug}.csv`, 'text/csv', toCsv(rows));
        } else {
            rows.push(['system', 'id', 'label']);
            for (const system of scopedSystems) {
                const units = [...(system.units ?? [])].sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
                for (const unit of units) {
                    rows.push([system.label, unit.id, unit.label]);
                }
            }
            slug = this.scopeSlug();
            downloadFile(this.document, `rdio-scanner-units-${slug}.csv`, 'text/csv', toCsv(rows));
        }
    }

    private scopeSlug(): string {
        switch (this.scope.kind) {
            case 'all': return 'all-systems';
            case 'system': return slugify(this.systemLabel(this.scope.system));
            default: return slugify(this.scope.label);
        }
    }

    // The whole-config JSON export, moved verbatim from the retired
    // import/export config tool — the data-URI form keeps the output
    // byte-identical to files exported by older versions.
    private async exportConfig(): Promise<void> {
        const config = await this.adminService.getConfig();

        const file = encodeURIComponent(JSON.stringify(config)).replace(/%([0-9A-F]{2})/g, (_, c) => {
            return String.fromCharCode(parseInt(c, 16));
        });
        const fileName = 'rdio-scanner.json';
        const fileType = 'application/json';
        const fileUri = `data:${fileType};base64,${window.btoa(file)}`;

        const el = this.document.createElement('a');

        el.style.display = 'none';

        el.setAttribute('href', fileUri);
        el.setAttribute('download', fileName);

        this.document.body.appendChild(el);

        el.click();

        this.document.body.removeChild(el);
    }
}

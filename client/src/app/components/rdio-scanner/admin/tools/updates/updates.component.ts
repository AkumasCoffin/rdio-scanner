/*
 * *****************************************************************************
 * Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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

import { Component, OnInit } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import { AdminUpdateRelease, AdminUpdates, RdioScannerAdminService } from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-updates',
    styleUrls: ['./updates.component.scss'],
    templateUrl: './updates.component.html',
})
export class RdioScannerAdminUpdatesComponent implements OnInit {
    updates: AdminUpdates | undefined;

    customUrl = '';
    includePrereleases = true;
    includeReleases = true;
    branch = '';
    selectedVersion = '';

    loading = false;
    busy = false;
    status = '';

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) { }

    async ngOnInit(): Promise<void> {
        await this.load();
    }

    get branches(): string[] {
        const set = new Set<string>();
        (this.updates?.releases ?? []).forEach((r) => set.add(r.branch));
        return [...set].sort();
    }

    get filtered(): AdminUpdateRelease[] {
        return (this.updates?.releases ?? []).filter((r) =>
            (r.prerelease ? this.includePrereleases : this.includeReleases) &&
            (!this.branch || r.branch === this.branch));
    }

    get selectedRelease(): AdminUpdateRelease | undefined {
        return this.filtered.find((r) => r.version === this.selectedVersion);
    }

    label(r: AdminUpdateRelease): string {
        const tags: string[] = [];
        if (r.prerelease) { tags.push('prerelease'); }
        if (r.current) { tags.push('current'); }
        if (!r.hasAsset) { tags.push('no ' + (this.updates?.platform ?? '') + ' binary'); }
        return `${r.version} (${r.branch})` + (tags.length ? ` — ${tags.join(', ')}` : '');
    }

    onFilter(): void {
        if (!this.filtered.some((r) => r.version === this.selectedVersion)) {
            const first = this.filtered.find((r) => r.hasAsset) ?? this.filtered[0];
            this.selectedVersion = first?.version ?? '';
        }
    }

    async load(): Promise<void> {
        this.loading = true;
        this.status = '';
        try {
            this.updates = await this.adminService.getUpdates();
            if (this.updates) {
                this.customUrl = this.updates.customUrl || '';
                this.onFilter();
            }
        } catch (e) {
            this.status = this.errMsg(e, 'Could not load releases — check the update URL and network.');
        }
        this.loading = false;
    }

    async saveSource(): Promise<void> {
        this.busy = true;
        try {
            await this.adminService.setUpdateSource(this.customUrl.trim());
            await this.load();
            this.matSnackBar.open('Update source saved', '', { duration: 3000 });
        } catch (e) {
            this.status = this.errMsg(e, 'Could not save the update source.');
        }
        this.busy = false;
    }

    async download(): Promise<void> {
        if (!this.selectedVersion) {
            return;
        }
        this.busy = true;
        this.status = `Downloading ${this.selectedVersion}…`;
        try {
            const res = await this.adminService.downloadUpdate(this.selectedVersion);
            this.status = `Downloaded ${res.version} (${res.asset}). Press “Update & restart” to apply it.`;
            await this.refreshPending();
        } catch (e) {
            this.status = this.errMsg(e, 'Download failed.');
        }
        this.busy = false;
    }

    async apply(): Promise<void> {
        this.busy = true;
        this.status = 'Applying the update and restarting the server…';
        try {
            await this.adminService.applyUpdate();
        } catch (e) {
            // The HTTP request usually fails because the server restarts mid-flight.
        }
        this.status = 'Server is restarting with the new version. This page will reconnect shortly — reload it if it does not.';
        this.busy = false;
    }

    async cancel(): Promise<void> {
        this.busy = true;
        try {
            await this.adminService.cancelUpdate();
            this.status = 'Staged update discarded.';
            await this.refreshPending();
        } catch (e) {
            this.status = this.errMsg(e, 'Could not discard the staged update.');
        }
        this.busy = false;
    }

    private async refreshPending(): Promise<void> {
        try {
            this.updates = await this.adminService.getUpdates();
        } catch (e) {
            // keep the previous view on a transient error
        }
    }

    private errMsg(e: unknown, fallback: string): string {
        const err = e as { error?: { error?: string }; message?: string };
        return err?.error?.error || err?.message || fallback;
    }
}

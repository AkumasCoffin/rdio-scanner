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
import { AdminUpdates, RdioScannerAdminService } from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-updates',
    styleUrls: ['./updates.component.scss'],
    templateUrl: './updates.component.html',
})
export class RdioScannerAdminUpdatesComponent implements OnInit {
    updates: AdminUpdates | undefined;

    customUrl = '';
    prereleases = true;

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

    private apply(u: AdminUpdates | undefined): void {
        this.updates = u;
        if (u) {
            this.customUrl = u.customUrl || '';
            this.prereleases = u.prereleases;
        }
    }

    async load(): Promise<void> {
        this.loading = true;
        this.status = '';
        try {
            this.apply(await this.adminService.getUpdates());
        } catch (e) {
            this.status = this.errMsg(e, 'Could not load update status.');
        }
        this.loading = false;
    }

    async checkNow(): Promise<void> {
        this.busy = true;
        this.status = 'Checking for updates…';
        try {
            this.apply(await this.adminService.checkUpdates());
            this.status = '';
        } catch (e) {
            this.status = this.errMsg(e, 'Update check failed.');
        }
        this.busy = false;
    }

    async saveSettings(): Promise<void> {
        this.busy = true;
        this.status = 'Saving…';
        try {
            this.apply(await this.adminService.setUpdateSource(this.customUrl.trim(), this.prereleases));
            this.status = '';
            this.matSnackBar.open('Update settings saved', '', { duration: 3000 });
        } catch (e) {
            this.status = this.errMsg(e, 'Could not save update settings.');
        }
        this.busy = false;
    }

    // One click: download the available binary, then apply it and restart.
    async update(): Promise<void> {
        this.busy = true;
        try {
            if (!this.updates?.pending) {
                this.status = `Downloading ${this.updates?.available?.version ?? 'update'}…`;
                await this.adminService.downloadUpdate();
            }
            this.status = 'Applying the update and restarting the server…';
            try {
                await this.adminService.applyUpdate();
            } catch (e) {
                // The request usually fails because the server restarts mid-flight.
            }
            this.status = 'Server is restarting with the new version. This page will reconnect shortly — reload it if it does not.';
        } catch (e) {
            this.status = this.errMsg(e, 'Update failed.');
        }
        this.busy = false;
    }

    async discard(): Promise<void> {
        this.busy = true;
        try {
            await this.adminService.cancelUpdate();
            await this.load();
            this.status = 'Staged update discarded.';
        } catch (e) {
            this.status = this.errMsg(e, 'Could not discard the staged update.');
        }
        this.busy = false;
    }

    private errMsg(e: unknown, fallback: string): string {
        const err = e as { error?: { error?: string }; message?: string };
        return err?.error?.error || err?.message || fallback;
    }
}

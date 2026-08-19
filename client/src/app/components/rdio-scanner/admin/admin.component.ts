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

import { Component, OnDestroy, ViewChild, ViewEncapsulation } from '@angular/core';
import { MatDialog } from '@angular/material/dialog';
import { firstValueFrom } from 'rxjs';
import packageInfo from '../../../../../package.json';
import { AdminEvent, RdioScannerAdminService } from './admin.service';
import { ConfigSection, RdioScannerAdminConfigComponent } from './config/config.component';
import { RdioScannerAdminRestartDialogComponent } from './restart-dialog.component';

export type AdminTab = ConfigSection | 'dashboard' | 'plugins' | 'logs' | 'tools';

interface AdminTabDef {
    id: AdminTab;
    label: string;
    icon: string;
}

// One flat row of tabs replaces the old accordion-inside-accordion: the
// Config sections are promoted to top-level tabs alongside the server
// sections, in the order an admin most often needs them.
const ADMIN_TABS: AdminTabDef[] = [
    { id: 'dashboard', label: 'Dashboard', icon: 'analytics' },
    { id: 'options', label: 'Options', icon: 'tune' },
    { id: 'systems', label: 'Systems', icon: 'podcasts' },
    { id: 'groupsTags', label: 'Groups & Tags', icon: 'workspaces' },
    { id: 'access', label: 'Access', icon: 'manage_accounts' },
    { id: 'apiKeys', label: 'API Keys', icon: 'vpn_key' },
    { id: 'dirWatch', label: 'Dirwatch', icon: 'folder' },
    { id: 'downstreams', label: 'Downstreams', icon: 'share' },
    { id: 'plugins', label: 'Plugins', icon: 'extension' },
    { id: 'logs', label: 'Logs', icon: 'article' },
    { id: 'tools', label: 'Tools', icon: 'build' },
];

const CONFIG_TABS: AdminTab[] = ['options', 'systems', 'groupsTags', 'access', 'apiKeys', 'dirWatch', 'downstreams'];

@Component({
    encapsulation: ViewEncapsulation.None,
    selector: 'rdio-scanner-admin',
    styleUrls: ['./admin.component.scss'],
    templateUrl: './admin.component.html',
})
export class RdioScannerAdminComponent implements OnDestroy {
    @ViewChild('configComponent') configComponent: RdioScannerAdminConfigComponent | undefined;

    authenticated = this.adminService.authenticated;

    version = packageInfo.version;

    /** True from asking for a restart until the server answers again. */
    restarting = false;

    /** Set only when a restart was asked for and the server never returned. */
    restartError = '';

    activeTab: AdminTab = 'dashboard';

    // The section the always-alive config host shows. Kept separate from
    // activeTab so leaving for Logs and coming back returns to the same
    // config section.
    configSection: ConfigSection = 'options';

    private eventSubscription = this.adminService.event.subscribe(async (event: AdminEvent) => {
        if ('authenticated' in event) {
            this.authenticated = event.authenticated || false;

            if (!this.authenticated) {
                this.activeTab = 'dashboard';
            }
        }
    });

    constructor(
        private adminService: RdioScannerAdminService,
        private matDialog: MatDialog,
    ) { }

    /**
     * Restarts the server, waits for it to answer again, then reloads.
     *
     * Lives in the header rather than on the Plugins tab: plugins are the
     * usual reason to want it, but it restarts the whole server, and a
     * control's home should be the thing it acts on. The reload lands on the
     * login screen — admin sessions are held in memory, so none survives.
     */
    async restartServer(): Promise<void> {
        const confirmed = await firstValueFrom(
            this.matDialog.open(RdioScannerAdminRestartDialogComponent, {
                width: '28rem',
                maxWidth: '95vw',
            }).afterClosed(),
        );

        if (!confirmed) {
            return;
        }

        this.restarting = true;

        try {
            await this.adminService.restartServer();
            await this.adminService.waitForServer();
            window.location.reload();
        } catch (err) {
            this.restarting = false;
            // Either the request failed or the server never came back. The
            // second needs saying out loud rather than a spinner that stops.
            this.restartError = 'The server did not come back. Check it on the host.';
        }
    }

    get visibleTabs(): AdminTabDef[] {
        return ADMIN_TABS.filter((tab) => tab.id !== 'dirWatch' || !this.configComponent?.docker);
    }

    get isConfigTab(): boolean {
        return CONFIG_TABS.includes(this.activeTab);
    }

    ngOnDestroy(): void {
        this.eventSubscription.unsubscribe();
    }

    select(tab: AdminTab): void {
        this.activeTab = tab;

        if (CONFIG_TABS.includes(tab)) {
            this.configSection = tab as ConfigSection;
        }
    }

    // A config tab wears a warn badge while its slice of the form is invalid,
    // so a validation error is findable from any tab — the accordion used to
    // show this on the collapsed panel header.
    tabInvalid(tab: AdminTab): boolean {
        const form = this.configComponent?.form;

        if (!form) {
            return false;
        }

        switch (tab) {
            case 'groupsTags':
                return !!(form.get('groups')?.invalid || form.get('tags')?.invalid);
            case 'dashboard':
            case 'plugins':
            case 'logs':
            case 'tools':
                return false;
            default:
                return !!form.get(tab)?.invalid;
        }
    }

    async logout(): Promise<void> {
        await this.adminService.logout();
    }
}

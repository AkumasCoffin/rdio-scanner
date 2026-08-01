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
import {
    AdminAvailablePlugin,
    AdminPlugin,
    AdminPluginRepo,
    PluginConfigField,
    RdioScannerAdminService,
} from '../admin.service';

@Component({
    selector: 'rdio-scanner-admin-plugins',
    styleUrls: ['./plugins.component.scss'],
    templateUrl: './plugins.component.html',
})
export class RdioScannerAdminPluginsComponent implements OnInit {
    plugins: AdminPlugin[] = [];
    repos: AdminPluginRepo[] = [];
    available: AdminAvailablePlugin[] = [];
    branches: string[] = [];

    serverVersion = '';
    pluginsDir = '';

    selectedRepo = '';
    selectedBranch = '';

    newRepoUrl = '';
    newRepoToken = '';

    loading = false;
    busy = false;
    browsing = false;
    status = '';

    /** pluginId of the plugin whose settings form is open. */
    expandedConfig = '';

    /** Working copy of the open plugin's settings, so Cancel really cancels. */
    configDraft: { [key: string]: unknown } = {};

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) { }

    async ngOnInit(): Promise<void> {
        await this.load();
    }

    /** True when any installed plugin is waiting on a restart to take effect. */
    get restartRequired(): boolean {
        return this.plugins.some((plugin) => plugin.restartRequired);
    }

    /** True when browsing a repository that isn't the official one. */
    get browsingThirdParty(): boolean {
        const repo = this.repos.find((candidate) => candidate.url === this.selectedRepo);
        return !!repo && !repo.official;
    }

    /**
     * Branches other than the repository's mainline hold work in progress. The
     * admin panel lists them deliberately — that is where unreleased plugins
     * live — but says so.
     */
    get browsingUntestedBranch(): boolean {
        return !!this.selectedBranch && this.selectedBranch !== 'main' && this.selectedBranch !== 'master';
    }

    async load(): Promise<void> {
        this.loading = true;
        this.status = '';

        try {
            const response = await this.adminService.getPlugins();

            this.plugins = response.plugins || [];
            this.repos = response.repos || [];
            this.serverVersion = response.serverVersion;
            this.pluginsDir = response.pluginsDir;

            if (!this.selectedRepo && this.repos.length) {
                this.selectedRepo = this.repos[0].url;
                await this.loadBranches();
            }
        } catch (err) {
            this.status = this.errMsg(err, 'Could not load plugins.');
        }

        this.loading = false;
    }

    /** The Refresh button: re-reads everything and bypasses the listing cache. */
    async refresh(): Promise<void> {
        await this.load();
        await this.loadBranches(true);
    }

    async onRepoChange(): Promise<void> {
        this.branches = [];
        this.selectedBranch = '';
        this.available = [];
        await this.loadBranches();
    }

    async loadBranches(refresh = false): Promise<void> {
        if (!this.selectedRepo) {
            return;
        }

        this.browsing = true;
        this.status = '';

        try {
            const response = await this.adminService.getPluginBranches(this.selectedRepo, refresh);
            this.branches = response.branches || [];

            // Prefer the conventional mainline so the default view is the
            // tested one, even though every branch is offered.
            this.selectedBranch = this.branches.find((b) => b === 'main')
                || this.branches.find((b) => b === 'master')
                || this.branches[0]
                || '';

            if (this.selectedBranch) {
                await this.loadAvailable(refresh);
            }
        } catch (err) {
            this.status = this.errMsg(err, 'Could not list branches for that repository.');
        }

        this.browsing = false;
    }

    async loadAvailable(refresh = false): Promise<void> {
        if (!this.selectedRepo || !this.selectedBranch) {
            return;
        }

        this.browsing = true;
        this.status = '';

        try {
            const response = await this.adminService.getPluginsAvailable(this.selectedRepo, this.selectedBranch, refresh);
            this.available = response.available || [];
        } catch (err) {
            this.available = [];
            this.status = this.errMsg(err, 'Could not list plugins on that branch.');
        }

        this.browsing = false;
    }

    async install(entry: AdminAvailablePlugin): Promise<void> {
        if (!entry.compatible) {
            return;
        }

        if (!entry.official && !confirm(
            `Install "${entry.manifest.name}" from ${entry.repo}?\n\n` +
            `This is not the official repository. Do you trust it?`
        )) {
            return;
        }

        this.busy = true;
        this.status = `Installing ${entry.manifest.name}…`;

        try {
            await this.adminService.installPlugin(entry.repo, entry.branch, entry.manifest.id);
            this.matSnackBar.open(`${entry.manifest.name} installed. Restart Rdio Scanner to load it.`, '', { duration: 5000 });
            await this.load();
            await this.loadAvailable();
            this.status = '';
        } catch (err) {
            this.status = this.errMsg(err, 'Install failed.');
        }

        this.busy = false;
    }

    async toggle(plugin: AdminPlugin): Promise<void> {
        this.busy = true;

        try {
            await this.adminService.togglePlugin(plugin.pluginId, !plugin.enabled);
            await this.load();
        } catch (err) {
            this.status = this.errMsg(err, 'Could not change that plugin.');
        }

        this.busy = false;
    }

    async uninstall(plugin: AdminPlugin): Promise<void> {
        if (!confirm(
            `Uninstall "${plugin.name}"?\n\n` +
            `Its settings and data are kept, so reinstalling restores everything. ` +
            `Use "Purge data" afterwards if you want them removed.`
        )) {
            return;
        }

        this.busy = true;

        try {
            await this.adminService.uninstallPlugin(plugin.pluginId);
            this.matSnackBar.open(`${plugin.name} uninstalled. Its settings were kept.`, '', { duration: 5000 });
            await this.load();
            await this.loadAvailable();
        } catch (err) {
            this.status = this.errMsg(err, 'Uninstall failed.');
        }

        this.busy = false;
    }

    async purge(plugin: AdminPlugin): Promise<void> {
        if (!confirm(
            `Permanently delete all data and settings for "${plugin.name}"?\n\n` +
            `This cannot be undone.`
        )) {
            return;
        }

        this.busy = true;

        try {
            await this.adminService.purgePluginData(plugin.pluginId);
            this.matSnackBar.open(`${plugin.name} data purged.`, '', { duration: 5000 });
            await this.load();
        } catch (err) {
            this.status = this.errMsg(err, 'Purge failed.');
        }

        this.busy = false;
    }

    toggleConfig(plugin: AdminPlugin): void {
        if (this.expandedConfig === plugin.pluginId) {
            this.expandedConfig = '';
            this.configDraft = {};
            return;
        }

        this.expandedConfig = plugin.pluginId;
        this.configDraft = { ...(plugin.config || {}) };
    }

    configFields(plugin: AdminPlugin): PluginConfigField[] {
        return plugin.manifest?.config || [];
    }

    async saveConfig(plugin: AdminPlugin): Promise<void> {
        this.busy = true;

        try {
            await this.adminService.savePluginConfig(plugin.pluginId, this.configDraft);
            this.matSnackBar.open(`${plugin.name} settings saved.`, '', { duration: 3000 });
            this.expandedConfig = '';
            this.configDraft = {};
            await this.load();
        } catch (err) {
            this.status = this.errMsg(err, 'Could not save settings.');
        }

        this.busy = false;
    }

    async addRepo(): Promise<void> {
        const url = this.newRepoUrl.trim();
        if (!url) {
            return;
        }

        if (!confirm(
            `Add ${url} as a plugin repository?\n\n` +
            `You will be downloading and running code from it. Do you trust it?`
        )) {
            return;
        }

        this.busy = true;

        try {
            const existing = this.repos.filter((repo) => !repo.official).map((repo) => ({ url: repo.url }));
            existing.push({ url, ...(this.newRepoToken.trim() ? { token: this.newRepoToken.trim() } : {}) });

            const response = await this.adminService.savePluginRepos(existing);
            this.repos = response.repos || [];
            this.newRepoUrl = '';
            this.newRepoToken = '';
            this.status = '';
        } catch (err) {
            this.status = this.errMsg(err, 'Could not add that repository.');
        }

        this.busy = false;
    }

    async removeRepo(repo: AdminPluginRepo): Promise<void> {
        this.busy = true;

        try {
            const kept = this.repos
                .filter((candidate) => !candidate.official && candidate.url !== repo.url)
                .map((candidate) => ({ url: candidate.url }));

            const response = await this.adminService.savePluginRepos(kept);
            this.repos = response.repos || [];

            if (this.selectedRepo === repo.url) {
                this.selectedRepo = this.repos[0]?.url || '';
                await this.onRepoChange();
            }
        } catch (err) {
            this.status = this.errMsg(err, 'Could not remove that repository.');
        }

        this.busy = false;
    }

    trackByPluginId(_index: number, plugin: AdminPlugin): string {
        return plugin.pluginId;
    }

    trackByAvailableId(_index: number, entry: AdminAvailablePlugin): string {
        return entry.manifest.id;
    }

    private errMsg(err: unknown, fallback: string): string {
        const message = (err as { error?: { error?: string } })?.error?.error;
        return typeof message === 'string' && message ? message : fallback;
    }
}

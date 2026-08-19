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
import { MatDialog } from '@angular/material/dialog';
import { MatSnackBar } from '@angular/material/snack-bar';
import { firstValueFrom } from 'rxjs';
import { RdioScannerAdminPluginUninstallDialogComponent } from './uninstall-dialog.component';
import { RdioScannerAdminRestartDialogComponent } from './restart-dialog.component';
import {
    AdminAvailablePlugin,
    AdminPlugin,
    AdminPluginRepo,
    AdminPluginCost,
    AdminPluginUpdate,
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

    /** Per-point cost, most expensive first. Empty until a plugin has run. */
    cost: AdminPluginCost[] = [];

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

    /** Result of the last update check, keyed by pluginId. Empty until asked. */
    updates: { [pluginId: string]: AdminPluginUpdate } = {};

    checkingUpdates = false;

    /** True from asking for a restart until the server answers again. */
    restarting = false;

    /** Empty until a check has run, so "none found" can be said only once it has. */
    updateCheckRan = false;

    /** pluginId of the plugin whose settings form is open. */
    expandedConfig = '';

    /** Working copy of the open plugin's settings, so Cancel really cancels. */
    configDraft: { [key: string]: unknown } = {};

    constructor(
        private adminService: RdioScannerAdminService,
        private matDialog: MatDialog,
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
            this.cost = response.cost || [];

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

    /**
     * Asks the server to measure every installed plugin against the repository
     * and branch it came from.
     */
    async checkUpdates(): Promise<void> {
        this.checkingUpdates = true;
        this.status = '';

        try {
            const response = await this.adminService.getPluginUpdates();

            const next: { [pluginId: string]: AdminPluginUpdate } = {};
            for (const update of response.updates || []) {
                next[update.pluginId] = update;
            }
            this.updates = next;
            this.updateCheckRan = true;

            const failed = (response.updates || []).filter((update) => update.error);

            if (response.updateCount > 0) {
                this.matSnackBar.open(
                    `${response.updateCount} plugin update${response.updateCount === 1 ? '' : 's'} available.`,
                    '', { duration: 5000 });
            } else if (failed.length) {
                // Nothing found, but not everything was actually checked —
                // saying "up to date" here would be a claim we cannot make.
                this.status = `${failed.length} plugin${failed.length === 1 ? '' : 's'} could not be checked.`;
            } else {
                this.matSnackBar.open('All plugins are up to date.', '', { duration: 3000 });
            }
        } catch (err) {
            this.status = this.errMsg(err, 'Could not check for plugin updates.');
        }

        this.checkingUpdates = false;
    }

    /**
     * Restarts the server, then waits for it to answer again and reloads.
     *
     * The reload matters: this page is describing plugin state from before the
     * restart, and the whole point of restarting is that the state changes.
     * Leaving the old view up is how "did that actually work?" happens.
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
        this.status = '';

        try {
            await this.adminService.restartServer();
            await this.adminService.waitForServer();
            window.location.reload();
        } catch (err) {
            this.restarting = false;
            // Either the restart could not be asked for, or the server never
            // came back. The second needs saying out loud rather than being a
            // spinner that quietly stops.
            this.status = this.errMsg(err, 'The server did not come back. Check it on the host.');
        }
    }

    /** The pending update for a plugin, if the last check found one. */
    updateFor(plugin: AdminPlugin): AdminPluginUpdate | undefined {
        const update = this.updates[plugin.pluginId];
        return update && update.updateAvailable ? update : undefined;
    }

    /** Why a plugin could not be checked, if it could not be. */
    updateError(plugin: AdminPlugin): string {
        return this.updates[plugin.pluginId]?.error || '';
    }

    /**
     * The check result for a plugin that came back up to date.
     *
     * "Up to date" is a claim about one repository and one branch, and it is
     * only as good as which ones were consulted — a plugin installed from a
     * branch that never received a release is genuinely up to date against
     * that branch while looking stale next to the version everyone else has.
     * Showing what was checked turns an unqualified claim into one that can
     * be disagreed with.
     */
    upToDate(plugin: AdminPlugin): AdminPluginUpdate | undefined {
        const update = this.updates[plugin.pluginId];
        return update && !update.updateAvailable && !update.error ? update : undefined;
    }

    /** True when a check found updates for at least one plugin. */
    get updatesAvailable(): number {
        return Object.values(this.updates).filter((update) => update.updateAvailable).length;
    }

    /**
     * Installs the newer version over the top. The install path already keeps
     * the plugin's enabled state and its stored settings, so this is an update
     * rather than a reinstall.
     */
    async update(plugin: AdminPlugin): Promise<void> {
        const pending = this.updateFor(plugin);
        if (!pending) {
            return;
        }

        if (!pending.compatible) {
            this.status = `${plugin.name} ${pending.latestVersion} ${pending.incompatible || 'is not compatible with this server'}.`;
            return;
        }

        this.busy = true;
        this.status = `Updating ${plugin.name}…`;

        try {
            await this.adminService.installPlugin(pending.repo, pending.branch, plugin.pluginId);
            this.matSnackBar.open(
                `${plugin.name} updated to ${pending.latestVersion}. Restart Rdio Scanner to load it.`,
                '', { duration: 5000 });

            delete this.updates[plugin.pluginId];

            await this.load();
            this.status = '';
        } catch (err) {
            this.status = this.errMsg(err, `Could not update ${plugin.name}.`);
        }

        this.busy = false;
    }

    /** Updates every plugin the last check found a newer version for. */
    async updateAll(): Promise<void> {
        const pending = this.plugins.filter((plugin) => {
            const update = this.updateFor(plugin);
            return !!update && update.compatible;
        });

        for (const plugin of pending) {
            await this.update(plugin);
        }
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
        // One dialog naming both outcomes on its buttons, rather than two
        // browser confirms where the second answered "keep or delete your
        // data" with OK and Cancel — a mapping nobody should have to guess at,
        // on the one question here that cannot be undone.
        //
        // Purging is still asked at uninstall time because that is the only
        // time it can be answered: it needs the manifest to know which tables
        // were the plugin's, and the manifest goes with the registry row.
        const answer = await firstValueFrom(
            this.matDialog.open(RdioScannerAdminPluginUninstallDialogComponent, {
                data: { name: plugin.name },
                width: '32rem',
                maxWidth: '95vw',
            }).afterClosed(),
        );

        if (!answer) {
            return;
        }

        const purge: boolean = answer.purge === true;

        this.busy = true;

        try {
            await this.adminService.uninstallPlugin(plugin.pluginId, purge);
            this.matSnackBar.open(
                purge
                    ? `${plugin.name} uninstalled and its data deleted.`
                    : `${plugin.name} uninstalled. Its settings were kept.`,
                '', { duration: 5000 });
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

    /**
     * A plugin's measured cost, or undefined before it has done anything.
     * Nothing is shown for a plugin that has never been dispatched into —
     * zeroes would read as a measurement rather than an absence of one.
     */
    costFor(plugin: AdminPlugin): AdminPluginCost | undefined {
        return plugin.cost && plugin.cost.calls > 0 ? plugin.cost : undefined;
    }

    /** The points one plugin spends its time at, most expensive first. */
    costByPoint(plugin: AdminPlugin): AdminPluginCost[] {
        return this.cost.filter((entry) => entry.pluginId === plugin.pluginId && entry.calls > 0);
    }

    /**
     * Whether a plugin is worth an operator's attention: it is failing, timing
     * out, or being skipped for running out of time. Slow on its own is not a
     * fault — slow enough to be cut short is.
     */
    costIsConcerning(cost: AdminPluginCost | undefined): boolean {
        return !!cost && (cost.failures > 0 || cost.timeouts > 0 || cost.skipped > 0);
    }

    /**
     * Whether a password field has a stored value. The value itself is blanked
     * before it reaches us, so without this the form cannot tell a configured
     * key from an empty one and looks like the setting was lost.
     */
    secretIsSet(plugin: AdminPlugin, field: PluginConfigField): boolean {
        return !!plugin.configSet && !!plugin.configSet[field.key];
    }

    configFields(plugin: AdminPlugin): PluginConfigField[] {
        const fields = plugin.manifest?.config || [];

        // A field can declare that it only applies when another field holds a
        // particular value — one provider's credentials, say. Hiding is purely
        // visual: the draft still carries the value, so saving while a field is
        // hidden leaves it exactly as it was.
        return fields.filter((field) => {
            if (!field.showIf) return true;

            const current = this.configDraft[field.showIf.key];

            return field.showIf.equals.some((value) => value === current);
        });
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

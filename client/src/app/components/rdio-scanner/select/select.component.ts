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

import { Component, OnDestroy } from '@angular/core';
import { MatDialog } from '@angular/material/dialog';
import {
    RdioScannerAvoidOptions,
    RdioScannerBeepStyle,
    RdioScannerCategory,
    RdioScannerCategoryStatus,
    RdioScannerEvent,
    RdioScannerLivefeedMap,
    RdioScannerPreset,
    RdioScannerSystem,
} from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';
import { PresetDialogData, RdioScannerPresetDialogComponent } from './preset-dialog.component';

@Component({
    selector: 'rdio-scanner-select',
    styleUrls: [
        '../common.scss',
        './select.component.scss',
    ],
    templateUrl: './select.component.html',
})
export class RdioScannerSelectComponent implements OnDestroy {
    categories: RdioScannerCategory[] | undefined;

    map: RdioScannerLivefeedMap = {};

    presets: RdioScannerPreset[] = [];

    systems: RdioScannerSystem[] | undefined;

    tagsToggle: boolean | undefined;

    private eventSubscription = this.rdioScannerService.event.subscribe((event: RdioScannerEvent) => this.eventHandler(event));

    constructor(
        private rdioScannerService: RdioScannerService,
        private dialog: MatDialog,
    ) {
        this.loadPresets();
    }

    avoid(options?: RdioScannerAvoidOptions): void {
        if (options?.all == true) {
            this.rdioScannerService.beep(RdioScannerBeepStyle.Activate);

        } else if (options?.all == false) {
            this.rdioScannerService.beep(RdioScannerBeepStyle.Deactivate);

        } else if (options?.system !== undefined && options?.talkgroup !== undefined) {
            this.rdioScannerService.beep(this.map[options!.system.id][options!.talkgroup.id].active
                ? RdioScannerBeepStyle.Deactivate
                : RdioScannerBeepStyle.Activate
            );

        } else {
            this.rdioScannerService.beep(options?.status ? RdioScannerBeepStyle.Activate : RdioScannerBeepStyle.Deactivate);
        }

        this.rdioScannerService.avoid(options);
    }

    ngOnDestroy(): void {
        this.eventSubscription.unsubscribe();
    }

    toggle(category: RdioScannerCategory): void {
        if (category.status == RdioScannerCategoryStatus.On)
            this.rdioScannerService.beep(RdioScannerBeepStyle.Deactivate);
        else
            this.rdioScannerService.beep(RdioScannerBeepStyle.Activate);

        this.rdioScannerService.toggleCategory(category);
    }

    createPreset(): void {
        if (!this.systems) return;

        const dialogRef = this.dialog.open(RdioScannerPresetDialogComponent, {
            width: '600px',
            maxWidth: '90vw',
            data: {
                systems: this.systems,
                map: this.map,
            } as PresetDialogData,
        });

        dialogRef.afterClosed().subscribe((preset: RdioScannerPreset | undefined) => {
            if (preset) {
                this.rdioScannerService.savePreset(preset);
                this.loadPresets();
            }
        });
    }

    editPreset(preset: RdioScannerPreset): void {
        if (!this.systems) return;

        const dialogRef = this.dialog.open(RdioScannerPresetDialogComponent, {
            width: '600px',
            maxWidth: '90vw',
            data: {
                preset: preset,
                systems: this.systems,
                map: this.map,
            } as PresetDialogData,
        });

        dialogRef.afterClosed().subscribe((updatedPreset: RdioScannerPreset | undefined) => {
            if (updatedPreset) {
                this.rdioScannerService.savePreset(updatedPreset);
                this.loadPresets();
            }
        });
    }

    deletePreset(preset: RdioScannerPreset): void {
        if (confirm(`Delete preset "${preset.name}"?`)) {
            this.rdioScannerService.deletePreset(preset.id);
            this.loadPresets();
        }
    }

    applyPreset(preset: RdioScannerPreset, activate: boolean): void {
        this.rdioScannerService.applyPreset(preset, activate);
    }

    exportPresets(): void {
        const json = this.rdioScannerService.exportPresets();
        const blob = new Blob([json], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `rdio-scanner-presets-${new Date().toISOString().split('T')[0]}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }

    importPresets(): void {
        const input = document.createElement('input');
        input.type = 'file';
        input.accept = '.json';
        input.onchange = (event: Event) => {
            const target = event.target as HTMLInputElement;
            const file = target.files?.[0];
            if (file) {
                const reader = new FileReader();
                reader.onload = (e) => {
                    const text = e.target?.result as string;
                    const result = this.rdioScannerService.importPresets(text);
                    if (result.success) {
                        alert(`Successfully imported ${result.count} preset(s)`);
                        this.loadPresets();
                    } else {
                        alert(`Import failed: ${result.error}`);
                    }
                };
                reader.readAsText(file);
            }
        };
        input.click();
    }

    private loadPresets(): void {
        this.presets = this.rdioScannerService.getPresets();
    }

    private eventHandler(event: RdioScannerEvent): void {
        if (event.config) {
            this.tagsToggle = event.config.tagsToggle;
            this.systems = event.config.systems;
        }
        if (event.categories) this.categories = event.categories;
        if (event.map) {
            this.map = event.map;
            // Reload presets when map changes to ensure consistency
            this.loadPresets();
        }
    }
}

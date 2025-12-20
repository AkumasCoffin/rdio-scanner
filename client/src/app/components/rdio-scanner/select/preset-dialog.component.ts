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

import { Component, Inject } from '@angular/core';
import { FormBuilder, FormGroup, Validators } from '@angular/forms';
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';
import {
    RdioScannerLivefeedMap,
    RdioScannerPreset,
    RdioScannerSystem,
} from '../rdio-scanner';

export interface PresetDialogData {
    preset?: RdioScannerPreset;
    systems: RdioScannerSystem[];
    map: RdioScannerLivefeedMap;
}

@Component({
    selector: 'rdio-scanner-preset-dialog',
    templateUrl: './preset-dialog.component.html',
    styleUrls: ['./preset-dialog.component.scss'],
})
export class RdioScannerPresetDialogComponent {
    form: FormGroup;
    selectedTalkgroups: Array<{ systemId: number; talkgroupId: number }> = [];

    constructor(
        @Inject(MAT_DIALOG_DATA) public data: PresetDialogData,
        private dialogRef: MatDialogRef<RdioScannerPresetDialogComponent>,
        private formBuilder: FormBuilder,
    ) {
        this.form = this.formBuilder.group({
            name: [data.preset?.name || '', Validators.required],
        });

        if (data.preset) {
            this.selectedTalkgroups = [...data.preset.talkgroups];
        }
    }

    toggleTalkgroup(systemId: number, talkgroupId: number): void {
        const index = this.selectedTalkgroups.findIndex(
            tg => tg.systemId === systemId && tg.talkgroupId === talkgroupId
        );

        if (index >= 0) {
            this.selectedTalkgroups.splice(index, 1);
        } else {
            this.selectedTalkgroups.push({ systemId, talkgroupId });
        }
    }

    isTalkgroupSelected(systemId: number, talkgroupId: number): boolean {
        return this.selectedTalkgroups.some(
            tg => tg.systemId === systemId && tg.talkgroupId === talkgroupId
        );
    }

    toggleSystem(systemId: number, activate: boolean): void {
        const system = this.data.systems.find(s => s.id === systemId);
        if (!system) return;

        if (activate) {
            // Add all talkgroups from this system
            system.talkgroups.forEach(tg => {
                if (!this.isTalkgroupSelected(systemId, tg.id)) {
                    this.selectedTalkgroups.push({ systemId, talkgroupId: tg.id });
                }
            });
        } else {
            // Remove all talkgroups from this system
            this.selectedTalkgroups = this.selectedTalkgroups.filter(
                tg => tg.systemId !== systemId
            );
        }
    }

    isSystemFullySelected(systemId: number): boolean {
        const system = this.data.systems.find(s => s.id === systemId);
        if (!system || system.talkgroups.length === 0) return false;

        return system.talkgroups.every(tg => this.isTalkgroupSelected(systemId, tg.id));
    }

    isSystemPartiallySelected(systemId: number): boolean {
        const system = this.data.systems.find(s => s.id === systemId);
        if (!system) return false;

        const selected = system.talkgroups.filter(tg => this.isTalkgroupSelected(systemId, tg.id));
        return selected.length > 0 && selected.length < system.talkgroups.length;
    }

    selectAll(): void {
        this.selectedTalkgroups = [];
        this.data.systems.forEach(system => {
            system.talkgroups.forEach(tg => {
                this.selectedTalkgroups.push({ systemId: system.id, talkgroupId: tg.id });
            });
        });
    }

    deselectAll(): void {
        this.selectedTalkgroups = [];
    }

    save(): void {
        if (this.form.valid && this.selectedTalkgroups.length > 0) {
            const preset: RdioScannerPreset = {
                id: this.data.preset?.id || `preset-${Date.now()}`,
                name: this.form.value.name,
                talkgroups: this.selectedTalkgroups,
                createdAt: this.data.preset?.createdAt || Date.now(),
            };
            this.dialogRef.close(preset);
        }
    }

    cancel(): void {
        this.dialogRef.close();
    }
}


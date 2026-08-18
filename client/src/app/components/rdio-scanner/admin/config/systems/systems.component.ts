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

import { CdkDragDrop, moveItemInArray } from '@angular/cdk/drag-drop';
import { Component, Input, QueryList, ViewChildren } from '@angular/core';
import { FormArray, FormGroup } from '@angular/forms';
import { MatExpansionPanel } from '@angular/material/expansion';
import { RdioScannerAdminService } from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-systems',
    templateUrl: './systems.component.html',
})
export class RdioScannerAdminSystemsComponent {
    @Input() form: FormArray | undefined;

    // Master/detail selection: the list on the left, this system's editor on
    // the right. Held by identity so reorders don't change what's selected.
    selected: FormGroup | undefined;

    get systems(): FormGroup[] {
        const systems = this.form?.controls
            .sort((a, b) => (a.value.order || 0) - (b.value.order || 0)) as FormGroup[];

        // Auto-select the first system so the detail pane never opens empty,
        // and drop a selection whose system was removed (e.g. by an import
        // replacing the whole config).
        if (this.selected && !systems?.includes(this.selected)) {
            this.selected = undefined;
        }
        if (!this.selected && systems?.length) {
            this.selected = systems[0];
        }

        return systems;
    }

    @ViewChildren(MatExpansionPanel) private panels: QueryList<MatExpansionPanel> | undefined;

    constructor(private adminService: RdioScannerAdminService) { }

    add(): void {
        const system = this.adminService.newSystemForm();

        system.markAllAsTouched();

        this.form?.insert(0, system);

        this.form?.markAsDirty();

        this.selected = system;
    }

    closeAll(): void {
        this.panels?.forEach((panel) => panel.close());
    }

    drop(event: CdkDragDrop<FormGroup[]>): void {
        if (event.previousIndex !== event.currentIndex) {
            moveItemInArray(event.container.data, event.previousIndex, event.currentIndex);

            event.container.data.forEach((dat, idx) => dat.get('order')?.setValue(idx + 1, { emitEvent: false }));

            this.form?.markAsDirty();
        }
    }

    select(system: FormGroup): void {
        this.selected = system;
    }

    removeSelected(): void {
        if (!this.selected) {
            return;
        }

        const index = this.form?.controls.indexOf(this.selected) ?? -1;

        if (index >= 0) {
            this.form?.removeAt(index);
            this.form?.markAsDirty();
        }

        this.selected = undefined;
    }
}

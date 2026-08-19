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
    // Only ever assigned from user actions — never from a getter. A getter
    // that wrote to it (or sorted the live controls array in place) mutated
    // state during template evaluation, so every change-detection pass
    // scheduled another one and the tab locked the browser up.
    private selection: FormGroup | undefined;

    get systems(): FormGroup[] {
        return [...(this.form?.controls ?? [])]
            .sort((a, b) => (a.value.order || 0) - (b.value.order || 0)) as FormGroup[];
    }

    // The system the detail pane shows: the user's pick when it still exists
    // (an import can replace the whole config), else the first one, so the
    // pane never opens empty. Pure — computed, never assigned.
    get selected(): FormGroup | undefined {
        const systems = this.systems;

        if (this.selection && systems.includes(this.selection)) {
            return this.selection;
        }

        return systems[0];
    }

    @ViewChildren(MatExpansionPanel) private panels: QueryList<MatExpansionPanel> | undefined;

    constructor(private adminService: RdioScannerAdminService) { }

    add(): void {
        const system = this.adminService.newSystemForm();

        system.markAllAsTouched();

        this.form?.insert(0, system);

        this.form?.markAsDirty();

        this.selection = system;
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
        this.selection = system;
    }

    removeSelected(): void {
        const selected = this.selected;

        if (!selected) {
            return;
        }

        const index = this.form?.controls.indexOf(selected) ?? -1;

        if (index >= 0) {
            this.form?.removeAt(index);
            this.form?.markAsDirty();
        }

        this.selection = undefined;
    }
}

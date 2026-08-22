/*
 * *****************************************************************************
 * Copyright (C) 2019-2024 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
    selector: 'rdio-scanner-admin-patches',
    templateUrl: './patches.component.html',
})
export class RdioScannerAdminPatchesComponent {
    @Input() form: FormArray | undefined;

    get patches(): FormGroup[] {
        return this.form?.controls
            .sort((a, b) => a.value.order - b.value.order) as FormGroup[];
    }

    // Reached through the form root rather than passed in, the same way the
    // dirwatch editor finds them: the systems array is a sibling of this one.
    get systems(): FormGroup[] {
        const systems = this.form?.root.get('systems') as FormArray;

        return (systems?.controls || []) as FormGroup[];
    }

    @ViewChildren(MatExpansionPanel) private panels: QueryList<MatExpansionPanel> | undefined;

    constructor(private adminService: RdioScannerAdminService) { }

    add(): void {
        const patch = this.adminService.newPatchForm({ talkgroups: [] });

        patch.markAllAsTouched();

        this.form?.insert(0, patch);

        this.form?.markAsDirty();
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

    remove(index: number): void {
        this.form?.removeAt(index);

        this.form?.markAsDirty();
    }

    /** The talkgroups of the system a patch is on, for its two pickers. */
    talkgroupsOf(patch: FormGroup): FormGroup[] {
        const system = this.systems.find((s) => s.value.id === patch.value.systemId);
        const talkgroups = system?.get('talkgroups') as FormArray;

        return (talkgroups?.controls || []) as FormGroup[];
    }

    /** Only the chosen talkgroups can be the primary, so it picks from those. */
    membersOf(patch: FormGroup): FormGroup[] {
        const chosen: number[] = patch.value.talkgroups || [];

        return this.talkgroupsOf(patch).filter((talkgroup) => chosen.includes(talkgroup.value.id));
    }

    /**
     * Moving a patch to another system leaves its talkgroups pointing at ids
     * that system may not have, so the selection starts again.
     */
    systemChanged(patch: FormGroup): void {
        patch.get('talkgroups')?.setValue([]);

        patch.markAsDirty();
    }

    /**
     * The select reports its value in option order, but the list's order is
     * the ranking, so it must not reshuffle what the user arranged: members
     * still selected keep their places, new picks join at the bottom.
     */
    talkgroupsChanged(patch: FormGroup): void {
        const selected: number[] = patch.get('talkgroups')?.value || [];
        const previous: number[] = this.lastOrder.get(patch) || [];

        const ordered = previous.filter((id) => selected.includes(id))
            .concat(selected.filter((id) => !previous.includes(id)));

        this.lastOrder.set(patch, ordered);
        patch.get('talkgroups')?.setValue(ordered, { emitEvent: false });

        patch.markAsDirty();
    }

    /** The arranged order per patch, so the select cannot reshuffle it. */
    private lastOrder = new Map<FormGroup, number[]>();

    /** Moves one member up or down the ranking. */
    moveMember(patch: FormGroup, index: number, delta: number): void {
        const ordered: number[] = (patch.get('talkgroups')?.value || []).slice();
        const target = index + delta;

        if (target < 0 || target >= ordered.length) {
            return;
        }

        [ordered[index], ordered[target]] = [ordered[target], ordered[index]];

        this.lastOrder.set(patch, ordered);
        patch.get('talkgroups')?.setValue(ordered, { emitEvent: false });

        patch.markAsDirty();
    }

    /** Label for one member id, for the ranking list. */
    memberLabel(patch: FormGroup, id: number): string {
        const talkgroup = this.talkgroupsOf(patch).find((t) => t.value.id === id);

        return talkgroup ? `${id} — ${talkgroup.value.label}` : `${id}`;
    }

    labelOf(patch: FormGroup): string {
        const system = this.systems.find((s) => s.value.id === patch.value.systemId);
        const count = (patch.value.talkgroups || []).length;

        if (!system) {
            return patch.value.label || 'NewPatch';
        }

        return `${patch.value.label || 'NewPatch'} — ${system.value.label}, ${count} talkgroup${count === 1 ? '' : 's'}`;
    }
}

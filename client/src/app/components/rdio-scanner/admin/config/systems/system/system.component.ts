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
import { CdkVirtualScrollViewport } from '@angular/cdk/scrolling';
import { Component, EventEmitter, Input, Output, QueryList, ViewChild, ViewChildren } from '@angular/core';
import { FormArray, FormControl, FormGroup } from '@angular/forms';
import { MatExpansionPanel } from '@angular/material/expansion';
import { RdioScannerAdminService, Group, Tag } from '../../../admin.service';
import { LED_HEX } from '../../../../led-colors';

@Component({
    selector: 'rdio-scanner-admin-system',
    templateUrl: './system.component.html',
})
export class RdioScannerAdminSystemComponent {
    ledHex = LED_HEX;

    // Settings / Talkgroups / Units as tabs rather than stacked dropdowns.
    // Only the active pane is in the DOM, which keeps the virtual-scroll
    // lists off the page until asked for — the same laziness the expansion
    // panels' matExpansionPanelContent gave.
    tab: 'settings' | 'talkgroups' | 'units' = 'settings';

    // The root form is the whole config form, so the Options section's toggle
    // is readable from here — the second color selector follows it live.
    get dualLed(): boolean {
        return this.form.root.get('options')?.value?.dualLed === true;
    }

    // Marks the Settings panel header when any of the system's own fields are
    // invalid — talkgroups and units have their own panels and badges.
    get settingsInvalid(): boolean {
        return ['id', 'label', 'led', 'led2', 'autoPopulate', 'blacklists', 'delay', 'alert']
            .some((key) => this.form.get(key)?.invalid === true);
    }

    private formValue = new FormGroup({});

    @Input()
    set form(form: FormGroup) {
        this.formValue = form;
        this.refreshLists();
    }

    get form(): FormGroup {
        return this.formValue;
    }

    @Input() groups: Group[] = [];

    @Input() tags: Tag[] = [];

    @Output() add = new EventEmitter<void>();

    @Output() remove = new EventEmitter<void>();

    leds = this.adminService.getLeds();

    alerts = ['alert1', 'alert2', 'alert3', 'alert4', 'alert5', 'alert6', 'alert7', 'alert8', 'alert9'];

    talkgroups: FormGroup[] = [];

    filteredTalkgroups: FormGroup[] = [];

    selectedTalkgroup: FormGroup | undefined;

    talkgroupQuery = '';

    units: FormGroup[] = [];

    filteredUnits: FormGroup[] = [];

    selectedUnit: FormGroup | undefined;

    unitQuery = '';

    @ViewChild('talkgroupViewport') private talkgroupViewport: CdkVirtualScrollViewport | undefined;

    @ViewChild('unitViewport') private unitViewport: CdkVirtualScrollViewport | undefined;

    @ViewChildren(MatExpansionPanel) private panels: QueryList<MatExpansionPanel> | undefined;

    private static filter(controls: FormGroup[], query: string): FormGroup[] {
        const normalizedQuery = query.trim().toLocaleLowerCase();

        if (!normalizedQuery) {
            return controls;
        }

        return controls.filter((control) => {
            const id = `${control.get('id')?.value ?? ''}`;
            const label = `${control.get('label')?.value ?? ''}`.toLocaleLowerCase();

            return id.includes(normalizedQuery) || label.includes(normalizedQuery);
        });
    }

    constructor(private adminService: RdioScannerAdminService) { }

    addTalkgroup(): void {
        const talkgroups = this.form.get('talkgroups') as FormArray;
        const talkgroup = this.adminService.newTalkgroupForm();

        talkgroups.insert(0, talkgroup);

        this.form.markAsDirty();
        this.refreshLists();
        this.selectedTalkgroup = talkgroup;
        this.talkgroupViewport?.scrollToIndex(0);
    }

    addUnit(): void {
        const units = this.form.get('units') as FormArray;
        const unit = this.adminService.newUnitForm();

        units.insert(0, unit);

        this.form.markAsDirty();
        this.refreshLists();
        this.selectedUnit = unit;
        this.unitViewport?.scrollToIndex(0);
    }

    blacklistTalkgroup(talkgroup: FormGroup): void {
        const id = talkgroup.value.id;

        if (typeof id !== 'number') {
            return;
        }

        const blacklists = this.form?.get('blacklists') as FormControl;

        blacklists.setValue(blacklists.value?.trim() ? `${blacklists.value},${id}` : `${id}`);

        this.removeTalkgroup(talkgroup);
    }

    closeAll(): void {
        this.panels?.forEach((panel) => panel.close());
    }

    dropTalkgroup(event: CdkDragDrop<FormGroup[]>): void {
        this.drop(event, this.talkgroups, this.talkgroupViewport, this.talkgroupQuery);
    }

    dropUnit(event: CdkDragDrop<FormGroup[]>): void {
        this.drop(event, this.units, this.unitViewport, this.unitQuery);
    }

    filterTalkgroups(event: Event): void {
        this.talkgroupQuery = (event.target as HTMLInputElement).value;
        this.filteredTalkgroups = RdioScannerAdminSystemComponent.filter(this.talkgroups, this.talkgroupQuery);
        this.talkgroupViewport?.scrollToIndex(0);
    }

    filterUnits(event: Event): void {
        this.unitQuery = (event.target as HTMLInputElement).value;
        this.filteredUnits = RdioScannerAdminSystemComponent.filter(this.units, this.unitQuery);
        this.unitViewport?.scrollToIndex(0);
    }

    removeTalkgroup(talkgroup: FormGroup): void {
        this.removeControl('talkgroups', talkgroup);
        this.selectedTalkgroup = undefined;
        this.refreshLists();
    }

    removeUnit(unit: FormGroup): void {
        this.removeControl('units', unit);
        this.selectedUnit = undefined;
        this.refreshLists();
    }

    selectTalkgroup(talkgroup: FormGroup): void {
        this.selectedTalkgroup = talkgroup;
    }

    selectUnit(unit: FormGroup): void {
        this.selectedUnit = unit;
    }

    trackByControl(_index: number, control: FormGroup): FormGroup {
        return control;
    }

    private drop(
        event: CdkDragDrop<FormGroup[]>,
        controls: FormGroup[],
        viewport: CdkVirtualScrollViewport | undefined,
        query: string,
    ): void {
        if (query || event.previousIndex === event.currentIndex) {
            return;
        }

        const renderedStart = viewport?.getRenderedRange().start ?? 0;
        const previousIndex = renderedStart + event.previousIndex;
        const currentIndex = renderedStart + event.currentIndex;

        moveItemInArray(controls, previousIndex, currentIndex);

        controls.forEach((control, index) => {
            control.get('order')?.setValue(index + 1, { emitEvent: false });
        });

        this.form.markAsDirty();
    }

    private refreshLists(): void {
        const talkgroups = this.form.get('talkgroups') as FormArray | null;
        const units = this.form.get('units') as FormArray | null;

        this.talkgroups = [...talkgroups?.controls ?? []]
            .sort((a, b) => (a.value.order || 0) - (b.value.order || 0)) as FormGroup[];
        this.units = [...units?.controls ?? []]
            .sort((a, b) => (a.value.order || 0) - (b.value.order || 0)) as FormGroup[];
        this.filteredTalkgroups = RdioScannerAdminSystemComponent.filter(this.talkgroups, this.talkgroupQuery);
        this.filteredUnits = RdioScannerAdminSystemComponent.filter(this.units, this.unitQuery);
    }

    private removeControl(name: 'talkgroups' | 'units', control: FormGroup): void {
        const controls = this.form.get(name) as FormArray;
        const index = controls.controls.indexOf(control);

        if (index === -1) {
            return;
        }

        controls.removeAt(index);
        controls.markAsDirty();
    }
}

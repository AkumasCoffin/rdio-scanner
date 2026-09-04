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
import { Component, ElementRef, EventEmitter, Input, Output, QueryList, ViewChild, ViewChildren } from '@angular/core';
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
    // Only the active pane is in the DOM, which keeps the lists off the page
    // until asked for — the same laziness the expansion panels'
    // matExpansionPanelContent gave.
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

    /**
     * What a plugin mounted in the admin-system slot is handed.
     *
     * Rebuilt only when the editor is pointed at a different system, never on
     * every change-detection pass: the slot re-renders whenever this input
     * changes by reference, and a fresh object each pass would tear down and
     * remount the plugin's UI continuously.
     *
     * It is therefore a snapshot taken when the system was selected. That is
     * the right granularity for what plugins key off — the saved system id —
     * and a system whose id is being edited has not been saved under the new
     * one yet.
     */
    pluginContext: { id: number | null; label: string } = { id: null, label: '' };

    private formValue = new FormGroup({});

    @Input()
    set form(form: FormGroup) {
        this.formValue = form;

        // One editor instance now serves every system (the master/detail pane
        // swaps this input), so per-system view state has to be dropped here.
        // Carrying a selection across the swap left the editor pointed at the
        // previous system's talkgroup: edits went to the old system, and
        // Delete/Blacklist acted on an id the new system doesn't contain.
        //
        // The active tab is deliberately NOT reset: comparing the same tab
        // across systems is the reason to click through the list, and being
        // thrown back to Settings each time makes that a chore.
        this.pluginContext = {
            id: form?.get('id')?.value ?? null,
            label: form?.get('label')?.value ?? '',
        };

        this.selectedTalkgroup = undefined;
        this.selectedUnit = undefined;
        this.talkgroupQuery = '';
        this.unitQuery = '';

        this.clearTalkgroupSelection();
        this.clearUnitSelection();

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

    // Everything currently picked out. The single-selection field above is
    // still what the one-talkgroup editor binds to; this is what the bulk
    // editor and the bulk delete act on, and the two are kept in step —
    // a lone selection is an array of one.
    selectedTalkgroups: FormGroup[] = [];

    talkgroupQuery = '';

    units: FormGroup[] = [];

    filteredUnits: FormGroup[] = [];

    selectedUnit: FormGroup | undefined;

    selectedUnits: FormGroup[] = [];

    unitQuery = '';

    // Where a shift-click measures its range from: the last row picked
    // without shift held.
    private talkgroupAnchor: FormGroup | undefined;

    private unitAnchor: FormGroup | undefined;

    @ViewChild('talkgroupList') private talkgroupList: ElementRef<HTMLElement> | undefined;

    @ViewChild('unitList') private unitList: ElementRef<HTMLElement> | undefined;

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
        this.selectedTalkgroups = [talkgroup];
        this.selectedTalkgroup = talkgroup;
        this.talkgroupAnchor = talkgroup;
        if (this.talkgroupList) {
            this.talkgroupList.nativeElement.scrollTop = 0;
        }
    }

    addUnit(): void {
        const units = this.form.get('units') as FormArray;
        const unit = this.adminService.newUnitForm();

        units.insert(0, unit);

        this.form.markAsDirty();
        this.refreshLists();
        this.selectedUnits = [unit];
        this.selectedUnit = unit;
        this.unitAnchor = unit;
        if (this.unitList) {
            this.unitList.nativeElement.scrollTop = 0;
        }
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
        this.drop(event, this.talkgroups, this.talkgroupQuery);
    }

    dropUnit(event: CdkDragDrop<FormGroup[]>): void {
        this.drop(event, this.units, this.unitQuery);
    }

    filterTalkgroups(event: Event): void {
        this.talkgroupQuery = (event.target as HTMLInputElement).value;
        this.filteredTalkgroups = RdioScannerAdminSystemComponent.filter(this.talkgroups, this.talkgroupQuery);
        if (this.talkgroupList) {
            this.talkgroupList.nativeElement.scrollTop = 0;
        }
    }

    filterUnits(event: Event): void {
        this.unitQuery = (event.target as HTMLInputElement).value;
        this.filteredUnits = RdioScannerAdminSystemComponent.filter(this.units, this.unitQuery);
        if (this.unitList) {
            this.unitList.nativeElement.scrollTop = 0;
        }
    }

    removeTalkgroup(talkgroup: FormGroup): void {
        this.removeControl('talkgroups', talkgroup);
        this.clearTalkgroupSelection();
        this.refreshLists();
    }

    removeUnit(unit: FormGroup): void {
        this.removeControl('units', unit);
        this.clearUnitSelection();
        this.refreshLists();
    }

    selectTalkgroup(talkgroup: FormGroup, event?: MouseEvent): void {
        this.selectedTalkgroups = this.pick(
            this.filteredTalkgroups, this.selectedTalkgroups, talkgroup, event, 'talkgroupAnchor');

        // The single editor follows the selection while there is exactly one
        // thing selected, and gets out of the way otherwise.
        this.selectedTalkgroup = this.selectedTalkgroups.length === 1 ? this.selectedTalkgroups[0] : undefined;
    }

    selectUnit(unit: FormGroup, event?: MouseEvent): void {
        this.selectedUnits = this.pick(this.filteredUnits, this.selectedUnits, unit, event, 'unitAnchor');

        this.selectedUnit = this.selectedUnits.length === 1 ? this.selectedUnits[0] : undefined;
    }

    isTalkgroupSelected(talkgroup: FormGroup): boolean {
        return this.selectedTalkgroups.includes(talkgroup);
    }

    isUnitSelected(unit: FormGroup): boolean {
        return this.selectedUnits.includes(unit);
    }

    /**
     * Works out the new selection for a click.
     *
     * Ctrl (or Cmd) adds and removes one row, shift takes everything between
     * the anchor and the clicked row, and a plain click replaces the
     * selection — the arrangement every file list uses, so it needs no
     * explaining. The range is measured over the filtered list, which is what
     * is on screen: shift-clicking across a search result selects what the
     * user can see between the two rows, not what the search hid.
     */
    private pick(
        visible: FormGroup[],
        selected: FormGroup[],
        clicked: FormGroup,
        event: MouseEvent | undefined,
        anchorField: 'talkgroupAnchor' | 'unitAnchor',
    ): FormGroup[] {
        const anchor = this[anchorField];

        if (event?.shiftKey && anchor && visible.includes(anchor)) {
            const from = visible.indexOf(anchor);
            const to = visible.indexOf(clicked);

            // The anchor stays put, so dragging the range back and forth
            // keeps re-measuring from the same row rather than creeping.
            return visible.slice(Math.min(from, to), Math.max(from, to) + 1);
        }

        this[anchorField] = clicked;

        if (event?.ctrlKey || event?.metaKey) {
            return selected.includes(clicked)
                ? selected.filter((control) => control !== clicked)
                : [...selected, clicked];
        }

        return [clicked];
    }

    // ------------------------------------------------------------ bulk edit

    /**
     * The value to show for a field across the selection: the shared value
     * when they all agree, undefined when they do not. Undefined renders as
     * an empty control, so a mixed field says "mixed" by showing nothing
     * rather than by claiming one row's value stands for all of them.
     */
    sharedValue(field: string): any {
        const values = this.selectedTalkgroups.map((talkgroup) => talkgroup.get(field)?.value);

        if (!values.length) {
            return undefined;
        }

        return values.every((value) => value === values[0]) ? values[0] : undefined;
    }

    /** Writes one field across every selected talkgroup. */
    applyToSelection(field: string, value: any): void {
        this.selectedTalkgroups.forEach((talkgroup) => {
            const control = talkgroup.get(field);

            if (control) {
                control.setValue(value);
                control.markAsDirty();
            }
        });

        this.form.markAsDirty();
    }

    /** Reads a bulk number input, where an empty box means "clear it". */
    applyNumberToSelection(field: string, event: Event): void {
        const raw = (event.target as HTMLInputElement).value.trim();

        this.applyToSelection(field, raw === '' ? null : Number(raw));
    }

    removeSelectedTalkgroups(): void {
        this.selectedTalkgroups.forEach((talkgroup) => this.removeControl('talkgroups', talkgroup));

        this.clearTalkgroupSelection();
        this.refreshLists();
    }

    removeSelectedUnits(): void {
        this.selectedUnits.forEach((unit) => this.removeControl('units', unit));

        this.clearUnitSelection();
        this.refreshLists();
    }

    clearTalkgroupSelection(): void {
        this.selectedTalkgroups = [];
        this.selectedTalkgroup = undefined;
        this.talkgroupAnchor = undefined;
    }

    clearUnitSelection(): void {
        this.selectedUnits = [];
        this.selectedUnit = undefined;
        this.unitAnchor = undefined;
    }

    /** Adds every row the current search matched to the selection. */
    selectAllTalkgroups(): void {
        this.selectedTalkgroups = [...this.filteredTalkgroups];
        this.selectedTalkgroup = this.selectedTalkgroups.length === 1 ? this.selectedTalkgroups[0] : undefined;
    }

    selectAllUnits(): void {
        this.selectedUnits = [...this.filteredUnits];
        this.selectedUnit = this.selectedUnits.length === 1 ? this.selectedUnits[0] : undefined;
    }

    trackByControl(_index: number, control: FormGroup): FormGroup {
        return control;
    }

    private drop(event: CdkDragDrop<FormGroup[]>, controls: FormGroup[], query: string): void {
        if (query || event.previousIndex === event.currentIndex) {
            return;
        }

        moveItemInArray(controls, event.previousIndex, event.currentIndex);

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

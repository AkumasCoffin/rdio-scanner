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

import { Component, ElementRef, OnDestroy, OnInit, ViewChild } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import { Subscription } from 'rxjs';
import { StreamItem, StreamLayout, STREAM_ITEM_TYPES, streamItemLabel } from './stream-layout';
import { StreamLayoutService } from './stream-layout.service';

// The Stream-settings menu, shown on the main page (as a dialog). Every change
// here is mirrored live to the /stream window via StreamLayoutService.
@Component({
    selector: 'rdio-scanner-stream-settings',
    styleUrls: ['./stream-settings.component.scss'],
    templateUrl: './stream-settings.component.html',
})
export class RdioScannerStreamSettingsComponent implements OnDestroy, OnInit {
    layout: StreamLayout = this.streamLayoutService.getLayout();

    readonly itemTypes = STREAM_ITEM_TYPES;

    @ViewChild('importFile') private importFile: ElementRef<HTMLInputElement> | undefined;

    private sub: Subscription | undefined;

    constructor(
        private streamLayoutService: StreamLayoutService,
        private matSnackBar: MatSnackBar,
    ) { }

    ngOnInit(): void {
        this.sub = this.streamLayoutService.changes.subscribe((layout) => (this.layout = layout));
    }

    ngOnDestroy(): void {
        this.sub?.unsubscribe();
    }

    trackItem(_index: number, item: StreamItem): string {
        return item.id;
    }

    itemLabel(type: string): string {
        return streamItemLabel(type);
    }

    setBgColor(value: string): void {
        this.streamLayoutService.update({ bgColor: value });
    }

    setMoveMode(enabled: boolean): void {
        this.streamLayoutService.update({ moveMode: enabled });
    }

    setGridSize(value: number): void {
        const size = Math.max(2, Math.min(200, Math.round(value || 0)));
        this.streamLayoutService.update({ gridSize: size });
    }

    addItem(type: string): void {
        this.streamLayoutService.addItem(type);
    }

    removeItem(id: string): void {
        this.streamLayoutService.removeItem(id);
    }

    setItemColor(id: string, value: string): void {
        this.streamLayoutService.updateItem(id, { color: value });
    }

    reset(): void {
        this.streamLayoutService.reset();
    }

    // Download the current layout as a JSON file.
    exportConfig(): void {
        const json = this.streamLayoutService.exportLayout();
        const blob = new Blob([json], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'rdio-scanner-stream-layout.json';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }

    triggerImport(): void {
        this.importFile?.nativeElement.click();
    }

    onImportFile(event: Event): void {
        const input = event.target as HTMLInputElement;
        const file = input.files?.[0];
        if (!file) {
            return;
        }

        const reader = new FileReader();
        reader.onload = () => {
            const result = this.streamLayoutService.importLayout(String(reader.result ?? ''));
            this.matSnackBar.open(
                result.success ? 'Stream layout imported' : `Import failed: ${result.error}`,
                '',
                { duration: 2500 },
            );
            input.value = '';
        };
        reader.onerror = () => {
            this.matSnackBar.open('Could not read file', '', { duration: 2500 });
            input.value = '';
        };
        reader.readAsText(file);
    }
}

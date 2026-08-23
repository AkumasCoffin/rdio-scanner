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
 * ****************************************************************************
 */

import { Component, Inject } from '@angular/core';
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';
import { ImportMode } from '../import-merge';

export interface MergeDialogData {
    /** What is being imported, so the wording names it. */
    noun: string;
    /** How many CSV rows land on an id the target already has. */
    existing: number;
    /** How many are new to the target. */
    new: number;
}

/**
 * Asked when a CSV overlaps what is already configured.
 *
 * The import used to answer this by itself — always overwrite — which is
 * right for a round trip through a spreadsheet and wrong for a wholesale
 * list dropped on top of talkgroups that were tuned by hand. Both readings
 * of "import this file" are reasonable, so the file cannot settle it and the
 * question comes here, with the row counts, before anything is changed.
 */
@Component({
    selector: 'rdio-scanner-admin-import-merge-dialog',
    templateUrl: './merge-dialog.component.html',
    styleUrls: ['./merge-dialog.component.scss'],
})
export class RdioScannerAdminImportMergeDialogComponent {
    constructor(
        @Inject(MAT_DIALOG_DATA) public data: MergeDialogData,
        private dialogRef: MatDialogRef<RdioScannerAdminImportMergeDialogComponent>,
    ) { }

    close(mode: ImportMode): void {
        this.dialogRef.close({ mode });
    }

    cancel(): void {
        this.dialogRef.close();
    }
}

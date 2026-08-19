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

import { Component } from '@angular/core';
import { MatDialogRef } from '@angular/material/dialog';

/**
 * Confirms a server restart.
 *
 * Worth confirming rather than acting on the click: a restart drops every
 * live listener and interrupts any upload in flight, and the button sits on a
 * page people visit to read about plugins.
 */
@Component({
    selector: 'rdio-scanner-admin-restart-dialog',
    templateUrl: './restart-dialog.component.html',
    styleUrls: ['./restart-dialog.component.scss'],
})
export class RdioScannerAdminRestartDialogComponent {
    constructor(private dialogRef: MatDialogRef<RdioScannerAdminRestartDialogComponent>) { }

    confirm(): void {
        this.dialogRef.close(true);
    }

    cancel(): void {
        this.dialogRef.close(false);
    }
}

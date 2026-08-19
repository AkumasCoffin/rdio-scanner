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
import { MAT_DIALOG_DATA, MatDialogRef } from '@angular/material/dialog';

export interface UninstallDialogData {
    name: string;
}

/**
 * The uninstall confirmation.
 *
 * One dialog, not two. This used to be a pair of browser confirms where the
 * second asked about the plugin's data and answered it with OK and Cancel —
 * so "Cancel" meant "keep the data and carry on uninstalling", which is the
 * opposite of what Cancel means everywhere else. Naming the two outcomes on
 * the buttons removes the guess.
 */
@Component({
    selector: 'rdio-scanner-admin-plugin-uninstall-dialog',
    templateUrl: './uninstall-dialog.component.html',
    styleUrls: ['./uninstall-dialog.component.scss'],
})
export class RdioScannerAdminPluginUninstallDialogComponent {
    constructor(
        @Inject(MAT_DIALOG_DATA) public data: UninstallDialogData,
        private dialogRef: MatDialogRef<RdioScannerAdminPluginUninstallDialogComponent>,
    ) { }

    /**
     * purge tells the server whether to drop the plugin's tables too.
     *
     * Asked here because here is the only place it can be answered: purging
     * needs the manifest to know which tables were the plugin's, and the
     * manifest goes with the registry row. There is no "uninstall now, decide
     * later" — that just orphans the tables.
     */
    close(purge: boolean): void {
        this.dialogRef.close({ purge });
    }

    cancel(): void {
        this.dialogRef.close();
    }
}

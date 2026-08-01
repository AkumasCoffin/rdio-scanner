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

import { Component, Input } from '@angular/core';
import { FormGroup } from '@angular/forms';

@Component({
    selector: 'rdio-scanner-admin-options',
    styleUrls: ['./options.component.scss'],
    templateUrl: './options.component.html',
})
export class RdioScannerAdminOptionsComponent {
    @Input() form: FormGroup | undefined;

    // Groups and Tags sorting are mutually exclusive: switching one on turns
    // the other off, so the selector is either ungrouped or grouped one way.
    selectSortMode(mode: 'groups' | 'tags', checked: boolean): void {
        if (!checked) return;

        const otherControl = mode === 'groups' ? 'sortByTags' : 'sortByGroups';
        this.form?.get(otherControl)?.setValue(false);
    }

    // Reported by the server. Audio conversion is a no-op without an ffmpeg
    // binary on PATH, and nothing in the UI used to say so — the only signal
    // was a single line in the log.
    @Input() ffmpegAvailable = true;

    // Platform-appropriate install command, resolved server-side.
    @Input() ffmpegInstallHint = '';

    // Only worth warning about when conversion is actually switched on;
    // "Disabled" plus no ffmpeg is a consistent state, not a problem.
    get ffmpegMissingAndNeeded(): boolean {
        return !this.ffmpegAvailable && this.form?.get('audioConversion')?.value !== 0;
    }

}

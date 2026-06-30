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

import { Component, OnDestroy, OnInit } from '@angular/core';
import { Subscription } from 'rxjs';
import { StreamLayout, STREAM_ELEMENTS } from './stream-layout';
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

    readonly elements = STREAM_ELEMENTS;

    private sub: Subscription | undefined;

    constructor(private streamLayoutService: StreamLayoutService) { }

    ngOnInit(): void {
        this.sub = this.streamLayoutService.changes.subscribe((layout) => (this.layout = layout));
    }

    ngOnDestroy(): void {
        this.sub?.unsubscribe();
    }

    openStreamWindow(): void {
        window.open('stream', 'rdio-scanner-stream', 'noopener');
    }

    setTextColor(value: string): void {
        this.streamLayoutService.update({ textColor: value });
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

    isVisible(key: string): boolean {
        return !!this.layout.elements[key]?.visible;
    }

    toggleVisible(key: string, visible: boolean): void {
        this.streamLayoutService.setVisible(key, visible);
    }

    reset(): void {
        this.streamLayoutService.reset();
    }
}

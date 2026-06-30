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

import { ChangeDetectorRef, Component, OnDestroy, OnInit } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatSnackBar } from '@angular/material/snack-bar';
import { Subscription } from 'rxjs';
import { RdioScannerEvent } from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';
import { RdioScannerMainComponent } from '../main/main.component';
import { StreamLayout } from './stream-layout';
import { StreamLayoutService } from './stream-layout.service';

// The /stream page. A stripped-down, OBS-friendly clone of the main LCD that:
//   - mirrors the main page's talkgroup selection / avoid / hold / auto-jump
//     live (via RdioScannerService follower mode) — no on-screen controls;
//   - auto-starts its OWN livefeed with audio (the one thing it does NOT
//     follow from the main page) so OBS can capture it;
//   - renders black-on-white by default so the background can be chroma-keyed;
//   - lets every element be repositioned (drag, Shift to grid-snap) and
//     shown/hidden via the Stream-settings menu on the main page.
//
// It extends RdioScannerMainComponent purely to reuse all of its LCD data
// plumbing (event handling + updateDisplay computing callSystem, callTalkgroup,
// listeners, queueDelayLabel, etc). Its own template/styles replace the main
// controls entirely.
@Component({
    selector: 'rdio-scanner-stream',
    styleUrls: ['./stream.component.scss'],
    templateUrl: './stream.component.html',
})
export class RdioScannerStreamComponent extends RdioScannerMainComponent implements OnDestroy, OnInit {
    // Current layout (colors, move mode, per-element visibility + position).
    layout: StreamLayout = this.streamLayoutService.getLayout();

    // Start overlay: hidden once the user has clicked Start (the gesture that
    // unlocks browser audio) and the livefeed is running.
    started = false;

    // Our own references to the base's injected deps — the base declares them
    // `private`, so we can't reach them through `this` in the subclass.
    private svc: RdioScannerService;
    private cdr: ChangeDetectorRef;

    private layoutSub: Subscription | undefined;
    private streamEventSub: Subscription | undefined;

    // Active drag state, set on pointerdown over an element in move mode.
    private dragKey: string | null = null;
    private dragStartX = 0;
    private dragStartY = 0;
    private dragOrigX = 0;
    private dragOrigY = 0;
    private boundMove = (e: PointerEvent) => this.onDragMove(e);
    private boundUp = (e: PointerEvent) => this.onDragEnd(e);

    constructor(
        private streamLayoutService: StreamLayoutService,
        rdioScannerService: RdioScannerService,
        matSnackBar: MatSnackBar,
        ngChangeDetectorRef: ChangeDetectorRef,
        ngFormBuilder: FormBuilder,
    ) {
        super(rdioScannerService, matSnackBar, ngChangeDetectorRef, ngFormBuilder);
        this.svc = rdioScannerService;
        this.cdr = ngChangeDetectorRef;
    }

    override ngOnInit(): void {
        // Reuse all of the main component's data wiring (clock, event
        // subscription, display computation, stored settings).
        super.ngOnInit();

        // Become a sync follower BEFORE anything subscribes, so we apply the
        // leader's state and never broadcast our own.
        this.svc.enableFollowerMode();

        this.layoutSub = this.streamLayoutService.changes.subscribe((layout) => {
            this.layout = layout;
            this.cdr.detectChanges();
        });

        // Stream-specific reactions on top of the inherited (private) handler.
        this.streamEventSub = this.svc.event.subscribe((event: RdioScannerEvent) => this.streamEventHandler(event));

        // Pull the leader's current holds/selection immediately (holds aren't
        // persisted to localStorage, so a fresh window can't read them).
        this.svc.requestSyncState();
    }

    override ngOnDestroy(): void {
        this.layoutSub?.unsubscribe();
        this.streamEventSub?.unsubscribe();
        this.detachDragListeners();
        this.svc.disableFollowerMode();
        super.ngOnDestroy();
    }

    // True while the start overlay should cover the screen: before the user
    // has started, or whenever the server is demanding an unlock code.
    get showOverlay(): boolean {
        return !this.started || this.auth;
    }

    // User clicked "Start". This click is the gesture that unlocks the audio
    // context (the service listens on document for the first pointer/keydown),
    // so kicking off the livefeed here will actually produce sound.
    startStream(): void {
        if (this.auth) {
            // Need the unlock code first — focus handled by the template.
            return;
        }
        this.svc.startLivefeed();
        this.started = true;
        this.cdr.detectChanges();
    }

    private streamEventHandler(event: RdioScannerEvent): void {
        // If auth was required and has now cleared (valid PIN accepted), the
        // overlay flips back to the Start button automatically via showOverlay.
        // Nothing else to do here yet, but keep the hook for clarity/future use.
        if ('config' in event) {
            this.cdr.detectChanges();
        }
    }

    // Template accessors — keep the markup terse and null-safe.
    elVisible(key: string): boolean {
        return !!this.layout.elements[key]?.visible;
    }

    elX(key: string): number {
        return this.layout.elements[key]?.x ?? 0;
    }

    elY(key: string): number {
        return this.layout.elements[key]?.y ?? 0;
    }

    // ---------------------------------------------------------------------
    // Drag-to-move (only active when layout.moveMode is on)
    // ---------------------------------------------------------------------

    onDragStart(key: string, event: PointerEvent): void {
        if (!this.layout.moveMode) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();

        const el = this.layout.elements[key];
        if (!el) {
            return;
        }

        this.dragKey = key;
        this.dragStartX = event.clientX;
        this.dragStartY = event.clientY;
        this.dragOrigX = el.x;
        this.dragOrigY = el.y;

        window.addEventListener('pointermove', this.boundMove);
        window.addEventListener('pointerup', this.boundUp);
    }

    private onDragMove(event: PointerEvent): void {
        if (!this.dragKey) {
            return;
        }

        let x = this.dragOrigX + (event.clientX - this.dragStartX);
        let y = this.dragOrigY + (event.clientY - this.dragStartY);

        // Hold Shift to snap to the grid.
        if (event.shiftKey && this.layout.gridSize > 0) {
            const g = this.layout.gridSize;
            x = Math.round(x / g) * g;
            y = Math.round(y / g) * g;
        }

        // Keep elements from being dragged off the top/left into oblivion.
        x = Math.max(0, x);
        y = Math.max(0, y);

        this.streamLayoutService.setPosition(this.dragKey, x, y);
    }

    private onDragEnd(_event: PointerEvent): void {
        this.dragKey = null;
        this.detachDragListeners();
    }

    private detachDragListeners(): void {
        window.removeEventListener('pointermove', this.boundMove);
        window.removeEventListener('pointerup', this.boundUp);
    }
}

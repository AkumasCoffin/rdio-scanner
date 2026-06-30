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
import { StreamItem, StreamLayout } from './stream-layout';
import { StreamLayoutService } from './stream-layout.service';

// The /stream page. An OBS-friendly, instance-based canvas clone of the main
// LCD that:
//   - mirrors the main page's talkgroup selection / avoid / hold / auto-jump
//     live (via RdioScannerService follower mode); the main page also remote-
//     controls its skip/replay/pause;
//   - auto-starts its OWN livefeed with audio (the one thing it does NOT follow
//     from the main page) so OBS can capture it;
//   - renders white-on-black by default so the background can be chroma-keyed;
//   - lets users add/remove/move/resize/recolor items via the Stream-settings
//     menu on the main page (live-synced over StreamLayoutService).
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
    // Current layout (bgColor, move mode, items).
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

    // Active drag/resize state, set on pointerdown over an item in move mode.
    private gestureId: string | null = null;
    private gestureMode: 'move' | 'resize' | null = null;
    private gestureStartX = 0;
    private gestureStartY = 0;
    private gestureOrigX = 0;
    private gestureOrigY = 0;
    private gestureOrigW = 0;
    private gestureOrigH = 0;
    private boundMove = (e: PointerEvent) => this.onGestureMove(e);
    private boundUp = () => this.onGestureEnd();

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
        this.detachGestureListeners();
        this.svc.disableFollowerMode();
        super.ngOnDestroy();
    }

    trackItem(_index: number, item: StreamItem): string {
        return item.id;
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

    removeItem(item: StreamItem, event?: Event): void {
        event?.stopPropagation();
        this.streamLayoutService.removeItem(item.id);
    }

    private streamEventHandler(event: RdioScannerEvent): void {
        // If auth was required and has now cleared (valid PIN accepted), the
        // overlay flips back to the Start button automatically via showOverlay.
        if ('config' in event) {
            this.cdr.detectChanges();
        }
    }

    // ---------------------------------------------------------------------
    // Drag-to-move / drag-to-resize (only active when layout.moveMode is on)
    // ---------------------------------------------------------------------

    onDragStart(item: StreamItem, event: PointerEvent): void {
        this.beginGesture(item, event, 'move');
    }

    onResizeStart(item: StreamItem, event: PointerEvent): void {
        this.beginGesture(item, event, 'resize');
    }

    private beginGesture(item: StreamItem, event: PointerEvent, mode: 'move' | 'resize'): void {
        if (!this.layout.moveMode) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();

        this.gestureId = item.id;
        this.gestureMode = mode;
        this.gestureStartX = event.clientX;
        this.gestureStartY = event.clientY;
        this.gestureOrigX = item.x;
        this.gestureOrigY = item.y;
        this.gestureOrigW = item.w;
        this.gestureOrigH = item.h;

        window.addEventListener('pointermove', this.boundMove);
        window.addEventListener('pointerup', this.boundUp);
    }

    private onGestureMove(event: PointerEvent): void {
        if (!this.gestureId || !this.gestureMode) {
            return;
        }

        const dx = event.clientX - this.gestureStartX;
        const dy = event.clientY - this.gestureStartY;
        const snap = (n: number): number => {
            if (event.shiftKey && this.layout.gridSize > 0) {
                return Math.round(n / this.layout.gridSize) * this.layout.gridSize;
            }
            return Math.round(n);
        };

        if (this.gestureMode === 'move') {
            const x = Math.max(0, snap(this.gestureOrigX + dx));
            const y = Math.max(0, snap(this.gestureOrigY + dy));
            this.streamLayoutService.updateItem(this.gestureId, { x, y });
        } else {
            const w = Math.max(20, snap(this.gestureOrigW + dx));
            const h = Math.max(16, snap(this.gestureOrigH + dy));
            this.streamLayoutService.updateItem(this.gestureId, { w, h });
        }
    }

    private onGestureEnd(): void {
        this.gestureId = null;
        this.gestureMode = null;
        this.detachGestureListeners();
    }

    private detachGestureListeners(): void {
        window.removeEventListener('pointermove', this.boundMove);
        window.removeEventListener('pointerup', this.boundUp);
    }
}

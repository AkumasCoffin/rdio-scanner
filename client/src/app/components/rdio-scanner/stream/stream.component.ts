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

import { DOCUMENT } from '@angular/common';
import { ChangeDetectorRef, Component, ElementRef, Inject, OnDestroy, OnInit, ViewChild } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatSnackBar } from '@angular/material/snack-bar';
import { Subscription } from 'rxjs';
import { RdioScannerEvent } from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';
import { RdioScannerMainComponent } from '../main/main.component';
import {
    StreamItem,
    StreamItemType,
    StreamLayout,
    STREAM_FONTS,
    STREAM_FONTS_HREF,
    STREAM_ITEM_TYPES,
    streamItemLabel,
} from './stream-layout';
import { StreamLayoutService } from './stream-layout.service';

// The /stream page. An OBS-friendly, instance-based canvas clone of the main
// LCD. It mirrors the main page's selection/avoid/hold/auto-jump live and is
// remote-controlled (skip/replay/pause/mute/volume) by the main page; it auto-
// starts its OWN audio so OBS can capture it. All editing happens here on the
// canvas (right-click context menu) when the main page enables edit mode.
@Component({
    selector: 'rdio-scanner-stream',
    styleUrls: ['./stream.component.scss'],
    templateUrl: './stream.component.html',
})
export class RdioScannerStreamComponent extends RdioScannerMainComponent implements OnDestroy, OnInit {
    layout: StreamLayout = this.streamLayoutService.getLayout();

    readonly itemTypes: ReadonlyArray<StreamItemType> = STREAM_ITEM_TYPES;
    readonly fonts = STREAM_FONTS;

    // Start overlay: hidden once the user has clicked Start (the gesture that
    // unlocks browser audio) and the livefeed is running.
    started = false;

    // Context menu state.
    ctxOpen = false;
    ctxX = 0;
    ctxY = 0;
    ctxItem: StreamItem | null = null;
    private addX = 40;
    private addY = 40;

    @ViewChild('importFile') private importFile: ElementRef<HTMLInputElement> | undefined;

    private svc: RdioScannerService;
    private cdr: ChangeDetectorRef;
    private snack: MatSnackBar;

    private layoutSub: Subscription | undefined;
    private streamEventSub: Subscription | undefined;

    // Active drag/resize state, set on pointerdown over an item in edit mode.
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
    private boundDocClick = () => this.closeContext();

    // Manifest swap so installing /stream as a PWA opens /stream, not the
    // main page.
    private manifestLink: HTMLLinkElement | null = null;
    private prevManifestHref: string | null = null;

    constructor(
        private streamLayoutService: StreamLayoutService,
        rdioScannerService: RdioScannerService,
        matSnackBar: MatSnackBar,
        ngChangeDetectorRef: ChangeDetectorRef,
        ngFormBuilder: FormBuilder,
        @Inject(DOCUMENT) private document: Document,
    ) {
        super(rdioScannerService, matSnackBar, ngChangeDetectorRef, ngFormBuilder);
        this.svc = rdioScannerService;
        this.cdr = ngChangeDetectorRef;
        this.snack = matSnackBar;
    }

    override ngOnInit(): void {
        super.ngOnInit();

        this.svc.enableFollowerMode();

        this.layoutSub = this.streamLayoutService.changes.subscribe((layout) => {
            this.layout = layout;
            this.cdr.detectChanges();
        });

        this.streamEventSub = this.svc.event.subscribe((event: RdioScannerEvent) => this.streamEventHandler(event));

        this.svc.requestSyncState();

        document.addEventListener('click', this.boundDocClick);

        // Point the PWA manifest at the stream-specific one so "Install app"
        // from this page yields an app that launches /stream.
        this.useStreamManifest();

        // Load the cool radio/display fonts (only on this page).
        this.loadStreamFonts();
    }

    private loadStreamFonts(): void {
        const id = 'rdio-stream-fonts';
        if (this.document.getElementById(id)) {
            return;
        }
        const link = this.document.createElement('link');
        link.id = id;
        link.rel = 'stylesheet';
        link.href = STREAM_FONTS_HREF;
        this.document.head.appendChild(link);
    }

    override ngOnDestroy(): void {
        this.layoutSub?.unsubscribe();
        this.streamEventSub?.unsubscribe();
        this.detachGestureListeners();
        document.removeEventListener('click', this.boundDocClick);
        this.restoreManifest();
        this.svc.disableFollowerMode();
        super.ngOnDestroy();
    }

    private useStreamManifest(): void {
        const link = this.document.querySelector('link[rel="manifest"]') as HTMLLinkElement | null;
        if (link) {
            this.manifestLink = link;
            this.prevManifestHref = link.getAttribute('href');
            link.setAttribute('href', 'stream.webmanifest');
        }
    }

    private restoreManifest(): void {
        if (this.manifestLink && this.prevManifestHref !== null) {
            this.manifestLink.setAttribute('href', this.prevManifestHref);
            this.manifestLink = null;
            this.prevManifestHref = null;
        }
    }

    trackItem(_index: number, item: StreamItem): string {
        return item.id;
    }

    itemLabel(type: string): string {
        return streamItemLabel(type);
    }

    get showOverlay(): boolean {
        return !this.started || this.auth;
    }

    startStream(): void {
        if (this.auth) {
            return;
        }
        this.svc.startLivefeed();
        this.started = true;
        this.cdr.detectChanges();
    }

    private streamEventHandler(event: RdioScannerEvent): void {
        if ('config' in event) {
            this.cdr.detectChanges();
        }
    }

    // ---------------------------------------------------------------------
    // Context menu (edit mode only)
    // ---------------------------------------------------------------------

    onCanvasContext(event: MouseEvent): void {
        if (!this.layout.moveMode) {
            return;
        }
        this.openContext(event, null);
    }

    onItemContext(item: StreamItem, event: MouseEvent): void {
        if (!this.layout.moveMode) {
            return;
        }
        event.stopPropagation();
        this.openContext(event, item);
    }

    private openContext(event: MouseEvent, item: StreamItem | null): void {
        event.preventDefault();
        this.ctxItem = item;
        this.addX = event.clientX;
        this.addY = event.clientY;
        // Keep the menu on-screen-ish.
        this.ctxX = Math.min(event.clientX, window.innerWidth - 230);
        this.ctxY = Math.min(event.clientY, window.innerHeight - 360);
        this.ctxOpen = true;
        this.cdr.detectChanges();
    }

    closeContext(): void {
        if (this.ctxOpen) {
            this.ctxOpen = false;
            this.cdr.detectChanges();
        }
    }

    // Stop the menu's own clicks from bubbling to the document close-handler.
    onMenuClick(event: Event): void {
        event.stopPropagation();
    }

    addItem(type: string): void {
        this.streamLayoutService.addItem(type, this.addX, this.addY);
        this.closeContext();
    }

    removeCtxItem(): void {
        if (this.ctxItem) {
            this.streamLayoutService.removeItem(this.ctxItem.id);
        }
        this.closeContext();
    }

    setCtxItemColor(value: string): void {
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { color: value });
        }
    }

    setCtxItemSize(value: number): void {
        if (this.ctxItem && Number.isFinite(value)) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { fontSize: Math.max(6, Math.min(200, Math.round(value))) });
        }
    }

    setCtxItemFont(value: string): void {
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { fontFamily: value });
        }
    }

    setCtxItemBold(bold: boolean): void {
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { bold });
        }
    }

    setCtxItemText(text: string): void {
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { text });
        }
    }

    setGridSize(value: number): void {
        if (Number.isFinite(value)) {
            this.streamLayoutService.update({ gridSize: Math.max(2, Math.min(200, Math.round(value))) });
        }
    }

    toggleBackground(enabled: boolean): void {
        this.streamLayoutService.update({ bgEnabled: enabled });
    }

    setBgColor(value: string): void {
        this.streamLayoutService.update({ bgColor: value });
    }

    resetLayout(): void {
        this.streamLayoutService.reset();
        this.closeContext();
    }

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
        this.closeContext();
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
            this.snack.open(
                result.success ? 'Stream layout imported' : `Import failed: ${result.error}`,
                '',
                { duration: 2500 },
            );
            input.value = '';
        };
        reader.onerror = () => {
            this.snack.open('Could not read file', '', { duration: 2500 });
            input.value = '';
        };
        reader.readAsText(file);
        this.closeContext();
    }

    // ---------------------------------------------------------------------
    // Drag-to-move / drag-to-resize (edit mode only)
    // ---------------------------------------------------------------------

    onDragStart(item: StreamItem, event: PointerEvent): void {
        this.beginGesture(item, event, 'move');
    }

    onResizeStart(item: StreamItem, event: PointerEvent): void {
        this.beginGesture(item, event, 'resize');
    }

    private beginGesture(item: StreamItem, event: PointerEvent, mode: 'move' | 'resize'): void {
        if (!this.layout.moveMode || event.button !== 0) {
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

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
import { RdioScannerCall, RdioScannerEvent } from '../rdio-scanner';
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
    streamItemMinH,
    streamItemMinW,
    streamItemTitle,
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
    @ViewChild('ctxMenu') private ctxMenuRef: ElementRef<HTMLElement> | undefined;
    @ViewChild('streamRoot') private streamRootRef: ElementRef<HTMLElement> | undefined;

    private svc: RdioScannerService;
    private cdr: ChangeDetectorRef;
    private snack: MatSnackBar;

    private layoutSub: Subscription | undefined;
    private streamEventSub: Subscription | undefined;

    // Multi-selection: ids of selected items (Ctrl+click toggles, Ctrl+drag
    // rubber-bands). Edits in the context menu apply to all selected.
    selectedIds = new Set<string>();

    // Rubber-band selection rectangle (client coords), shown while Ctrl+dragging
    // the canvas.
    selecting = false;
    selX = 0;
    selY = 0;
    selW = 0;
    selH = 0;
    private selStartX = 0;
    private selStartY = 0;
    private boundSelMove = (e: PointerEvent) => this.onSelectMove(e);
    private boundSelUp = () => this.onSelectEnd();

    // Active drag/resize state, set on pointerdown over an item in edit mode.
    private gestureId: string | null = null;
    private gestureMode: 'move' | 'resize' | null = null;
    private gestureStartX = 0;
    private gestureStartY = 0;
    private gestureOrigW = 0;
    private gestureOrigH = 0;
    // Original positions of every item being moved (one, or all selected).
    private moveTargets: { id: string; x: number; y: number }[] = [];
    private boundMove = (e: PointerEvent) => this.onGestureMove(e);
    private boundUp = () => this.onGestureEnd();
    private boundDocClick = () => this.closeContext();

    // Animation loop state for transcript auto-scroll + value marquee.
    private animRaf: number | undefined;
    private lastTime = 0;
    private lastTimeAt = 0;

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
            // Drop any selection once edit mode is turned off so it doesn't
            // linger (and reappear highlighted) the next time it's enabled.
            if (!layout.moveMode && this.selectedIds.size) {
                this.selectedIds.clear();
            }
            this.cdr.detectChanges();
        });

        this.streamEventSub = this.svc.event.subscribe((event: RdioScannerEvent) => this.streamEventHandler(event));

        this.svc.requestSyncState();

        document.addEventListener('click', this.boundDocClick);

        this.startAnim();

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
        window.removeEventListener('pointermove', this.boundSelMove);
        window.removeEventListener('pointerup', this.boundSelUp);
        document.removeEventListener('click', this.boundDocClick);
        if (this.animRaf !== undefined) {
            cancelAnimationFrame(this.animRaf);
        }
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

    // The title/label text for a type ('' when the type has no title option).
    titleOf(type: string): string {
        return streamItemTitle(type);
    }

    // How many of this item type are currently placed on the canvas. Used by
    // the Add menu to show counts and flag types that aren't on screen.
    countOf(type: string): number {
        return this.layout.items.reduce((n, i) => (i.type === type ? n + 1 : n), 0);
    }

    // A type is "missing" (highlighted in the Add menu) when none are on screen
    // — except custom text and frames, which are user-added decoration with no
    // inherent data, so they're never flagged as missing.
    isMissing(type: string): boolean {
        return this.countOf(type) === 0 && type !== 'text' && type !== 'frame';
    }

    // Hex equivalents of the main LCD's named LED colors.
    private static readonly LED_HEX: { [key: string]: string } = {
        blue: '#3b82f6',
        cyan: '#06b6d4',
        green: '#22c55e',
        magenta: '#a855f7',
        orange: '#f97316',
        red: '#ef4444',
        white: '#f9fafb',
        yellow: '#eab308',
    };

    // The LCD color of the currently/last playing call (from its talkgroup or
    // system LED), or null when there's nothing to colour by.
    private ledColor(): string | null {
        const call = this.displayCall;
        const led = (call?.talkgroupData?.led as string) || (call?.systemData?.led as string) || '';
        return RdioScannerStreamComponent.LED_HEX[led] ?? null;
    }

    // The effective color for an item: the live LCD color when "Match LCD
    // color" is on (falling back to the item's own color), else its own color.
    itemColor(item: StreamItem): string {
        if (item.useLedColor) {
            return this.ledColor() ?? item.color;
        }
        return item.color;
    }

    // Whether a conditionally-empty element currently has a value to show — so
    // its title isn't shown standing alone when there's nothing after it.
    hasContent(item: StreamItem): boolean {
        switch (item.type) {
            case 'uid':
                return !!this.callUnit;
            case 'tempAvoid':
                return this.tempAvoid > 0;
            default:
                return true;
        }
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

        // Track call progress (with a wall-clock stamp) so the transcript can
        // be scrolled smoothly between the once-a-second time events.
        if (typeof event.time === 'number') {
            this.lastTime = event.time;
            this.lastTimeAt = performance.now();
        }

        // Mirror our current call / progress to the main page so its LCD shows
        // what the stream is playing. Audio is stripped — the main page only
        // needs the metadata to render.
        const disp: {
            call?: RdioScannerCall;
            time?: number;
            queue?: number;
            queueTime?: number;
            queueJumped?: number;
            transcriptReady?: { id: number; transcript: string };
        } = {};
        let has = false;
        if ('call' in event) {
            disp.call = this.stripAudio(event.call);
            has = true;
        }
        if (typeof event.time === 'number') {
            disp.time = event.time;
            has = true;
        }
        if (typeof event.queue === 'number') {
            disp.queue = event.queue;
            has = true;
        }
        if (typeof event.queueTime === 'number') {
            disp.queueTime = event.queueTime;
            has = true;
        }
        if (typeof event.queueJumped === 'number') {
            disp.queueJumped = event.queueJumped;
            has = true;
        }
        if (event.transcriptReady) {
            disp.transcriptReady = event.transcriptReady;
            has = true;
        }
        if (has) {
            this.svc.broadcastFollowerDisplay(disp);
        }
    }

    private stripAudio(call: RdioScannerCall | undefined): RdioScannerCall | undefined {
        if (!call || !call.audio) {
            return call;
        }
        return { ...call, audio: undefined };
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
        // Right-clicking an item that isn't part of the current selection
        // selects just it; right-clicking within a multi-selection keeps it so
        // the edits apply to all selected.
        if (!this.selectedIds.has(item.id)) {
            this.selectedIds.clear();
            this.selectedIds.add(item.id);
        }
        this.openContext(event, item);
    }

    // The items a context-menu edit applies to: the whole selection, or just
    // the right-clicked item when nothing is selected.
    private targetIds(): string[] {
        return this.selectedIds.size ? [...this.selectedIds] : (this.ctxItem ? [this.ctxItem.id] : []);
    }

    // Count shown in the element-menu header so multi-edits are obvious.
    get selectedCount(): number {
        return this.selectedIds.size;
    }

    private openContext(event: MouseEvent, item: StreamItem | null): void {
        event.preventDefault();
        this.ctxItem = item;
        // Where a new element will be dropped (canvas-local).
        const p = this.localPoint(event.clientX, event.clientY);
        this.addX = p.x;
        this.addY = p.y;
        // Where the menu itself appears (fixed-positioned → viewport coords).
        this.ctxX = event.clientX;
        this.ctxY = event.clientY;
        this.ctxOpen = true;
        this.cdr.detectChanges();
        // Now that the menu has rendered with its real size, snap it fully into
        // view (the menu height varies a lot between the canvas/element menus).
        setTimeout(() => this.snapMenuIntoView());
    }

    private snapMenuIntoView(): void {
        const el = this.ctxMenuRef?.nativeElement;
        if (!el) {
            return;
        }
        const rect = el.getBoundingClientRect();
        const margin = 6;
        let x = this.ctxX;
        let y = this.ctxY;

        if (rect.right > window.innerWidth - margin) {
            x = Math.max(margin, window.innerWidth - rect.width - margin);
        }
        if (rect.bottom > window.innerHeight - margin) {
            y = Math.max(margin, window.innerHeight - rect.height - margin);
        }
        x = Math.max(margin, x);
        y = Math.max(margin, y);

        if (x !== this.ctxX || y !== this.ctxY) {
            this.ctxX = x;
            this.ctxY = y;
            this.cdr.detectChanges();
        }
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

    private applyToTargets(patch: Partial<StreamItem>): void {
        for (const id of this.targetIds()) {
            this.streamLayoutService.updateItem(id, patch);
        }
    }

    removeCtxItem(): void {
        const ids = this.targetIds();
        if (ids.length && window.confirm(`Remove ${ids.length} element${ids.length > 1 ? 's' : ''}?`)) {
            ids.forEach((id) => this.streamLayoutService.removeItem(id));
            this.selectedIds.clear();
        }
        this.closeContext();
    }

    setCtxItemColor(value: string): void {
        this.applyToTargets({ color: value });
    }

    setCtxItemSize(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ fontSize: Math.max(6, Math.min(200, Math.round(value))) });
        }
    }

    setCtxItemFont(value: string): void {
        this.applyToTargets({ fontFamily: value });
    }

    setCtxItemBold(bold: boolean): void {
        this.applyToTargets({ bold });
    }

    setCtxItemAutoScroll(autoScroll: boolean): void {
        this.applyToTargets({ autoScroll });
    }

    setCtxItemLed(useLedColor: boolean): void {
        this.applyToTargets({ useLedColor });
    }

    setCtxItemText(text: string): void {
        // Custom text is per-item — only write it to the right-clicked item.
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { text });
        }
    }

    setCtxItemTitleEnabled(titleEnabled: boolean): void {
        this.applyToTargets({ titleEnabled });
    }

    setCtxItemTitleColor(titleColor: string): void {
        this.applyToTargets({ titleColor });
    }

    setCtxItemTitleBold(titleBold: boolean): void {
        this.applyToTargets({ titleBold });
    }

    // Patch one column of the right-clicked history table, then refresh ctxItem
    // from the updated layout so further edits read fresh data.
    setHistCol(index: number, patch: Partial<{ title: string; visible: boolean; color: string; fontSize: number; bold: boolean }>): void {
        if (!this.ctxItem) {
            return;
        }
        if (typeof patch.fontSize === 'number') {
            if (!Number.isFinite(patch.fontSize)) {
                return;
            }
            patch = { ...patch, fontSize: Math.max(6, Math.min(120, Math.round(patch.fontSize))) };
        }
        const id = this.ctxItem.id;
        const cols = (this.ctxItem.historyCols || []).map((c, i) => (i === index ? { ...c, ...patch } : c));
        this.streamLayoutService.updateItem(id, { historyCols: cols });
        this.ctxItem = this.layout.items.find((i) => i.id === id) ?? this.ctxItem;
    }

    // Value for a history-table cell.
    histCell(call: RdioScannerCall | undefined, key: string): string {
        switch (key) {
            case 'system':
                return call?.systemData?.label || `${call?.system ?? ''}`;
            case 'talkgroup':
                return call?.talkgroupData?.label || `${call?.talkgroup ?? ''}`;
            case 'name':
                return call?.talkgroupData?.name || `${call?.frequency ?? ''}`;
            default:
                return '';
        }
    }

    setGridSize(value: number): void {
        if (Number.isFinite(value)) {
            this.streamLayoutService.update({ gridSize: Math.max(2, Math.min(200, Math.round(value))) });
        }
    }

    setBgColor(value: string): void {
        this.streamLayoutService.update({ bgColor: value });
    }

    resetLayout(): void {
        if (window.confirm('Reset the entire layout to defaults? This removes all your changes.')) {
            this.streamLayoutService.reset();
        }
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

    isSelected(id: string): boolean {
        return this.selectedIds.has(id);
    }

    // Convert a viewport (client) point to canvas-local coordinates — the same
    // space items are positioned in. Items / the rubber-band are absolutely
    // positioned within .stream-root, which is normally at (0,0) but this keeps
    // selection + placement correct even if it ever isn't.
    private localPoint(clientX: number, clientY: number): { x: number; y: number } {
        const rect = this.streamRootRef?.nativeElement.getBoundingClientRect();
        return { x: clientX - (rect?.left ?? 0), y: clientY - (rect?.top ?? 0) };
    }

    onDragStart(item: StreamItem, event: PointerEvent): void {
        if (!this.layout.moveMode) {
            return;
        }

        // Ctrl/Cmd+click an item toggles its selection instead of dragging.
        if (event.ctrlKey || event.metaKey) {
            event.preventDefault();
            event.stopPropagation();
            this.toggleSelected(item.id);
            this.cdr.detectChanges();
            return;
        }

        // Plain drag of an unselected item clears the selection first; dragging
        // a selected item moves the whole selection together.
        if (!this.selectedIds.has(item.id)) {
            this.selectedIds.clear();
        }

        this.beginGesture(item, event, 'move');
    }

    onResizeStart(item: StreamItem, event: PointerEvent): void {
        this.beginGesture(item, event, 'resize');
    }

    private toggleSelected(id: string): void {
        if (this.selectedIds.has(id)) {
            this.selectedIds.delete(id);
        } else {
            this.selectedIds.add(id);
        }
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
        this.gestureOrigW = item.w;
        this.gestureOrigH = item.h;

        // Move targets: the whole selection if this item is part of it, else
        // just this item.
        const moving = this.selectedIds.has(item.id) && this.selectedIds.size > 1
            ? this.layout.items.filter((i) => this.selectedIds.has(i.id))
            : [item];
        this.moveTargets = moving.map((i) => ({ id: i.id, x: i.x, y: i.y }));

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
            for (const t of this.moveTargets) {
                this.streamLayoutService.updateItem(t.id, {
                    x: Math.max(0, snap(t.x + dx)),
                    y: Math.max(0, snap(t.y + dy)),
                });
            }
        } else {
            const item = this.layout.items.find((i) => i.id === this.gestureId);
            const minW = item ? streamItemMinW(item.type) : 20;
            const minH = item ? streamItemMinH(item.type) : 16;
            const w = Math.max(minW, snap(this.gestureOrigW + dx));
            const h = Math.max(minH, snap(this.gestureOrigH + dy));
            this.streamLayoutService.updateItem(this.gestureId, { w, h });
        }
    }

    private onGestureEnd(): void {
        this.gestureId = null;
        this.gestureMode = null;
        this.moveTargets = [];
        this.detachGestureListeners();
    }

    private detachGestureListeners(): void {
        window.removeEventListener('pointermove', this.boundMove);
        window.removeEventListener('pointerup', this.boundUp);
    }

    // ---------------------------------------------------------------------
    // Auto-scroll: transcript vertical (in time with the call) + value marquee
    // ---------------------------------------------------------------------

    private startAnim(): void {
        const tick = (): void => {
            this.animFrame();
            this.animRaf = requestAnimationFrame(tick);
        };
        this.animRaf = requestAnimationFrame(tick);
    }

    private animFrame(): void {
        const root = this.streamRootRef?.nativeElement;
        if (!root) {
            return;
        }
        const now = performance.now();
        const writes: Array<() => void> = [];

        root.querySelectorAll('.stream-item').forEach((node) => {
            const el = node as HTMLElement;
            const type = el.dataset['type'] || '';
            const auto = el.dataset['autoscroll'] === '1';
            const content = el.querySelector('.item-content') as HTMLElement | null;
            if (!content) {
                return;
            }

            if (type === 'transcript') {
                if (!auto) {
                    return;
                }
                const maxV = content.scrollHeight - content.clientHeight;
                const call = this.call;
                if (maxV <= 0 || !call || !this.displayCall?.transcript) {
                    return;
                }
                const dur = this.svc.getCallDuration(call.id) ?? 0;
                if (dur <= 0) {
                    return;
                }
                const est = this.lastTime + (now - this.lastTimeAt) / 1000;
                const top = Math.max(0, Math.min(1, est / dur)) * maxV;
                writes.push(() => { content.scrollTop = top; });

            } else if (type !== 'history' && type !== 'frame' && type !== 'text') {
                // Single-line value: marquee horizontally when it overflows.
                const maxH = content.scrollWidth - content.clientWidth;
                if (!auto || maxH <= 1) {
                    if (content.scrollLeft !== 0) {
                        writes.push(() => { content.scrollLeft = 0; });
                    }
                    return;
                }
                const left = this.marqueePos(now, maxH);
                writes.push(() => { content.scrollLeft = left; });
            }
        });

        for (const w of writes) {
            w();
        }
    }

    // Ping-pong marquee position with a pause at each end.
    private marqueePos(now: number, max: number): number {
        const pxPerSec = 45;
        const pauseMs = 1500;
        const travelMs = (max / pxPerSec) * 1000;
        const t = now % ((travelMs + pauseMs) * 2);
        if (t < pauseMs) {
            return 0;
        }
        if (t < pauseMs + travelMs) {
            return ((t - pauseMs) / travelMs) * max;
        }
        if (t < pauseMs * 2 + travelMs) {
            return max;
        }
        return max - ((t - (pauseMs * 2 + travelMs)) / travelMs) * max;
    }

    // ---------------------------------------------------------------------
    // Rubber-band selection (Ctrl+drag on the canvas)
    // ---------------------------------------------------------------------

    onCanvasPointerDown(event: PointerEvent): void {
        if (!this.layout.moveMode || event.button !== 0) {
            return;
        }

        if (event.ctrlKey || event.metaKey) {
            // Start a rubber-band; existing selection is kept (additive).
            event.preventDefault();
            const p = this.localPoint(event.clientX, event.clientY);
            this.selecting = true;
            this.selStartX = p.x;
            this.selStartY = p.y;
            this.selX = p.x;
            this.selY = p.y;
            this.selW = 0;
            this.selH = 0;
            window.addEventListener('pointermove', this.boundSelMove);
            window.addEventListener('pointerup', this.boundSelUp);
        } else {
            // Plain click on empty canvas clears the selection.
            if (this.selectedIds.size) {
                this.selectedIds.clear();
                this.cdr.detectChanges();
            }
        }
    }

    private onSelectMove(event: PointerEvent): void {
        if (!this.selecting) {
            return;
        }
        const p = this.localPoint(event.clientX, event.clientY);
        this.selX = Math.min(this.selStartX, p.x);
        this.selY = Math.min(this.selStartY, p.y);
        this.selW = Math.abs(p.x - this.selStartX);
        this.selH = Math.abs(p.y - this.selStartY);
        this.cdr.detectChanges();
    }

    private onSelectEnd(): void {
        if (this.selecting) {
            // Select every item whose box intersects the rubber-band.
            const rx = this.selX;
            const ry = this.selY;
            const rr = this.selX + this.selW;
            const rb = this.selY + this.selH;
            for (const i of this.layout.items) {
                const overlap = !(i.x > rr || i.x + i.w < rx || i.y > rb || i.y + i.h < ry);
                if (overlap) {
                    this.selectedIds.add(i.id);
                }
            }
        }
        this.selecting = false;
        window.removeEventListener('pointermove', this.boundSelMove);
        window.removeEventListener('pointerup', this.boundSelUp);
        this.cdr.detectChanges();
    }
}

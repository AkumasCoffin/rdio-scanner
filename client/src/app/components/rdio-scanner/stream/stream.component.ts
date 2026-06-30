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
import { AfterViewChecked, ChangeDetectorRef, Component, ElementRef, Inject, OnDestroy, OnInit, ViewChild } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatSnackBar } from '@angular/material/snack-bar';
import { Subscription } from 'rxjs';
import { RdioScannerCall, RdioScannerEvent } from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';
import { RdioScannerMainComponent } from '../main/main.component';
import {
    StreamHistoryCol,
    StreamItem,
    StreamItemType,
    StreamLayout,
    STREAM_FONTS,
    STREAM_FONTS_HREF,
    STREAM_ITEM_TYPES,
    defaultShapePoints,
    streamIsBorder,
    streamIsFrame,
    streamIsShape,
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
export class RdioScannerStreamComponent extends RdioScannerMainComponent implements AfterViewChecked, OnDestroy, OnInit {
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

    // In-app confirm dialog (instead of window.confirm).
    confirm: { message: string; action: () => void } | null = null;

    // Clipboard for "Copy settings"/"Paste settings": styling fields lifted off
    // one element, ready to apply to others (geometry/identity/content excluded).
    copiedStyle: Partial<StreamItem> | null = null;
    copiedStyleType = '';

    @ViewChild('importFile') private importFile: ElementRef<HTMLInputElement> | undefined;
    @ViewChild('ctxMenu') private ctxMenuRef: ElementRef<HTMLElement> | undefined;
    @ViewChild('streamRoot') private streamRootRef: ElementRef<HTMLElement> | undefined;
    @ViewChild('linkLayer') private linkLayerRef: ElementRef<SVGSVGElement> | undefined;

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

    // Active drag/resize/vertex/divider state, set on pointerdown in edit mode.
    private gestureId: string | null = null;
    private gestureMode: 'move' | 'resize' | 'vertex' | 'divider' | null = null;
    private gestureStartX = 0;
    private gestureStartY = 0;
    private gestureOrigW = 0;
    private gestureOrigH = 0;
    // Vertex drag (shapable border): which corner, and the shape's corner points
    // (absolute canvas coords) captured at gesture start.
    private gestureVertexIndex = 0;
    private gestureVertexAbs: { x: number; y: number }[] = [];
    // Divider drag (shapable border): which divider, and its 0..1 position at start.
    private gestureDividerIndex = 0;
    private gestureDividerStartPos = 0;
    // Original positions of every item being moved (one, or all selected).
    private moveTargets: { id: string; x: number; y: number }[] = [];

    // Active alignment guides while dragging (canvas-local), or null.
    guideX: number | null = null;
    guideY: number | null = null;
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

    // Rectangular CSS border.
    isFrame(type: string): boolean {
        return streamIsFrame(type);
    }

    // Editable polygon border.
    isShape(type: string): boolean {
        return streamIsShape(type);
    }

    // Any decorative border (frame or shape): shares the band controls.
    isBorder(type: string): boolean {
        return streamIsBorder(type);
    }

    // The title/label text for a type ('' when the type has no title option).
    titleOf(type: string): string {
        return streamItemTitle(type);
    }

    // The displayed title text — the item's custom override or the type default.
    titleTextOf(item: StreamItem): string {
        return item.titleText || streamItemTitle(item.type);
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
        return this.countOf(type) === 0 && type !== 'text' && !streamIsBorder(type);
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

    // The LCD color of the CURRENTLY playing call (from its talkgroup or system
    // LED), or null when nothing is playing — so "match LCD color" elements fall
    // back to their set color between calls rather than holding the last color.
    private ledColor(): string | null {
        const call = this.call;
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

    // Inner border colour for a two-tone frame (optionally LED-driven).
    innerColor(item: StreamItem): string {
        if (item.centerUseLed) {
            return this.ledColor() ?? item.centerColor;
        }
        return item.centerColor;
    }

    // Title colour (optionally LED-driven, independent of the value colour).
    titleColorOf(item: StreamItem): string {
        if (item.titleUseLed) {
            return this.ledColor() ?? item.titleColor;
        }
        return item.titleColor;
    }

    middleColorOf(item: StreamItem): string {
        if (item.middleUseLed) {
            return this.ledColor() ?? item.middleColor;
        }
        return item.middleColor;
    }

    // Builds the inset box-shadow for a frame's middle + inner bands (the outer
    // band is the CSS border). Each enabled band is stacked inward.
    frameShadow(item: StreamItem): string | null {
        if (!streamIsFrame(item.type)) {
            return null;
        }
        const parts: string[] = [];
        let offset = 0;
        if (item.middleFill) {
            parts.push(`inset 0 0 0 ${offset + item.middleWidth}px ${this.middleColorOf(item)}`);
            offset += item.middleWidth;
        }
        if (item.centerFill) {
            parts.push(`inset 0 0 0 ${offset + item.innerWidth}px ${this.innerColor(item)}`);
            offset += item.innerWidth;
        }
        return parts.length ? parts.join(', ') : null;
    }

    // CSS border width + radius for the rectangular frame (uniform on all sides).
    frameBorderStyle(item: StreamItem): { [key: string]: number } {
        const w = streamIsFrame(item.type) ? item.borderWidth : 0;
        const r = streamIsFrame(item.type) ? item.cornerRadius : 0;
        return {
            'border-top-width.px': w, 'border-right-width.px': w,
            'border-bottom-width.px': w, 'border-left-width.px': w,
            'border-top-left-radius.px': r, 'border-top-right-radius.px': r,
            'border-bottom-right-radius.px': r, 'border-bottom-left-radius.px': r,
        };
    }

    // ---- Shapable border -----------------------------------------------------
    // A 'shape' element is an editable closed polygon (drag corners, bend edges)
    // drawn as up to three concentric SVG bands (outer / middle / inner). The
    // bands are rebuilt imperatively into the #linkLayer <svg> whenever a shape's
    // geometry or styling changes, and recomputed only when needed (sig check).
    private shapeSig = '';
    private shapeDirty = false;
    private shapeRenders: { outline: string; bands: { color: string; width: number }[]; dividerPaths: string[]; dividerBands: { color: string; width: number }[] }[] = [];

    ngAfterViewChecked(): void {
        this.recomputeShapes();
        if (this.shapeDirty) {
            this.shapeDirty = false;
            this.renderShapeLayer();
        }
    }

    get shapeItems(): StreamItem[] {
        return this.layout.items.filter((i) => i.type === 'shape');
    }

    // The shape's corner points (relative to the item), defaulting to a rectangle.
    shapePoints(item: StreamItem): { x: number; y: number }[] {
        return item.points && item.points.length >= 3 ? item.points : defaultShapePoints(item.w, item.h);
    }

    // Corner points in absolute canvas coordinates.
    private absShapePoints(item: StreamItem): { x: number; y: number }[] {
        return this.shapePoints(item).map((p) => ({ x: item.x + p.x, y: item.y + p.y }));
    }

    // Absolute midpoint of every edge (where the "add a bend" handles sit).
    edgeMids(item: StreamItem): { x: number; y: number }[] {
        const abs = this.absShapePoints(item);
        return abs.map((p, i) => {
            const q = abs[(i + 1) % abs.length];
            return { x: (p.x + q.x) / 2, y: (p.y + q.y) / 2 };
        });
    }

    // A shape's internal divider lines (vertical / horizontal).
    shapeDividers(item: StreamItem): { axis: 'h' | 'v'; pos: number }[] {
        return item.dividers ?? [];
    }

    // Where a divider's drag handle sits (its midpoint within the box).
    dividerHandleX(item: StreamItem, dv: { axis: 'h' | 'v'; pos: number }): number {
        return item.x + (dv.axis === 'v' ? dv.pos * item.w : item.w / 2);
    }

    dividerHandleY(item: StreamItem, dv: { axis: 'h' | 'v'; pos: number }): number {
        return item.y + (dv.axis === 'h' ? dv.pos * item.h : item.h / 2);
    }

    private recomputeShapes(): void {
        const shapes = this.shapeItems;
        const sig = shapes
            .map((s) => `${s.id}:${s.x},${s.y}:${JSON.stringify(s.points)}:${s.borderWidth},${s.cornerRadius},${s.color},${s.useLedColor ? 1 : 0}`
                + `|${s.middleFill ? 1 : 0},${s.middleWidth},${s.middleColor},${s.middleUseLed ? 1 : 0}`
                + `|${s.centerFill ? 1 : 0},${s.innerWidth},${s.centerColor},${s.centerUseLed ? 1 : 0}`
                + `|${s.hideOnCall ? 1 : 0},${s.hideOnIdle ? 1 : 0}:${s.w},${s.h}:${JSON.stringify(s.dividers)}`)
            .join('||') + '#' + (this.ledColor() ?? '') + (this.waitingForCall ? 'i' : 'c');
        if (sig === this.shapeSig) {
            return;
        }
        this.shapeSig = sig;
        this.shapeDirty = true;
        this.shapeRenders = [];
        for (const s of shapes) {
            // Respect the element's hide-while-call / hide-while-idle toggles.
            if (!this.dataVisible(s)) {
                continue;
            }
            const render = this.shapeRender(s);
            if (render) {
                this.shapeRenders.push(render);
            }
        }
    }

    // A shapable border is drawn as the rounded outline stroked once per band,
    // each stroke centred on the outline but clipped to the shape's interior so
    // only its inside half shows. Widest (outer colour) first, narrower bands
    // painted over it — giving outer/middle/inner bands aligned to the outline.
    // No polygon offsetting, so corners (including reflex bumps) can't spike.
    private shapeRender(item: StreamItem): { outline: string; bands: { color: string; width: number }[]; dividerPaths: string[]; dividerBands: { color: string; width: number }[] } | null {
        const abs = this.absShapePoints(item);
        const outline = this.roundedPath(abs, item.cornerRadius);
        if (!outline) {
            return null;
        }
        const wo = item.borderWidth;
        const wm = item.middleFill ? item.middleWidth : 0;
        const wi = item.centerFill ? item.innerWidth : 0;
        const total = wo + wm + wi;
        const bands: { color: string; width: number }[] = [];
        if (total > 0) {
            bands.push({ color: this.itemColor(item), width: 2 * total });
        }
        if (wm > 0) {
            bands.push({ color: this.middleColorOf(item), width: 2 * (wm + wi) });
        }
        if (wi > 0) {
            bands.push({ color: this.innerColor(item), width: 2 * wi });
        }
        // Internal dividers: full-extent lines clipped to the interior and drawn
        // under the bands so they meet the inner edge of the border cleanly.
        const dividerPaths: string[] = [];
        for (const dv of this.shapeDividers(item)) {
            if (dv.axis === 'v') {
                const x = item.x + dv.pos * item.w;
                dividerPaths.push(`M ${x.toFixed(2)} ${(item.y - 2).toFixed(2)} L ${x.toFixed(2)} ${(item.y + item.h + 2).toFixed(2)}`);
            } else {
                const y = item.y + dv.pos * item.h;
                dividerPaths.push(`M ${(item.x - 2).toFixed(2)} ${y.toFixed(2)} L ${(item.x + item.w + 2).toFixed(2)} ${y.toFixed(2)}`);
            }
        }
        // A divider's cross-section mirrors the border on each side: the band
        // touching each section is the inner colour (as on the border's interior
        // edge), then middle, with the outer colour down the centre — two borders
        // back to back. Painted widest-first so the colours stack correctly.
        const dividerBands: { color: string; width: number }[] = [];
        if (dividerPaths.length && total > 0) {
            if (wi > 0) {
                dividerBands.push({ color: this.innerColor(item), width: 2 * total });
            }
            if (wm > 0) {
                dividerBands.push({ color: this.middleColorOf(item), width: 2 * (wo + wm) });
            }
            if (wo > 0) {
                dividerBands.push({ color: this.itemColor(item), width: 2 * wo });
            }
        }
        return bands.length ? { outline, bands, dividerPaths, dividerBands } : null;
    }

    // Rebuild the #linkLayer SVG children directly (correct SVG namespace, so it
    // renders the same in every browser incl. OBS's embedded Chromium). Clip ids
    // are index-based so they're always valid + unique regardless of item ids.
    private renderShapeLayer(): void {
        const svg = this.linkLayerRef?.nativeElement;
        if (!svg) {
            return;
        }
        const NS = 'http://www.w3.org/2000/svg';
        while (svg.firstChild) {
            svg.removeChild(svg.firstChild);
        }
        svg.style.display = this.shapeRenders.length ? '' : 'none';
        if (!this.shapeRenders.length) {
            return;
        }
        const defs = this.document.createElementNS(NS, 'defs');
        svg.appendChild(defs);
        this.shapeRenders.forEach((r, index) => {
            const clipId = `rdio-shape-clip-${index}`;
            const clip = this.document.createElementNS(NS, 'clipPath');
            clip.setAttribute('id', clipId);
            clip.setAttribute('clipPathUnits', 'userSpaceOnUse');
            const clipPath = this.document.createElementNS(NS, 'path');
            clipPath.setAttribute('d', r.outline);
            clip.appendChild(clipPath);
            defs.appendChild(clip);
            // Dividers first (under the bands), clipped to the shape interior.
            // Each divider is stroked widest-band first so the band colours stack
            // into a symmetric outer/middle/inner stripe along the line.
            for (const dPath of r.dividerPaths) {
                for (const band of r.dividerBands) {
                    const line = this.document.createElementNS(NS, 'path');
                    line.setAttribute('d', dPath);
                    line.setAttribute('fill', 'none');
                    line.setAttribute('stroke', band.color);
                    line.setAttribute('stroke-width', String(band.width));
                    line.setAttribute('clip-path', `url(#${clipId})`);
                    line.style.clipPath = `url(#${clipId})`;
                    svg.appendChild(line);
                }
            }
            for (const band of r.bands) {
                const path = this.document.createElementNS(NS, 'path');
                path.setAttribute('d', r.outline);
                path.setAttribute('fill', 'none');
                path.setAttribute('stroke', band.color);
                path.setAttribute('stroke-width', String(band.width));
                path.setAttribute('stroke-linejoin', 'round');
                // Both the attribute and the CSS property — older WebKit honours
                // only one of them for SVG clip references.
                path.setAttribute('clip-path', `url(#${clipId})`);
                path.style.clipPath = `url(#${clipId})`;
                svg.appendChild(path);
            }
        });
    }

    // ---- Shape editing -------------------------------------------------------
    // Start dragging a corner of a shapable border.
    onVertexStart(item: StreamItem, index: number, event: PointerEvent): void {
        if (!this.layout.moveMode || event.button !== 0) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();
        this.gestureId = item.id;
        this.gestureMode = 'vertex';
        this.gestureVertexIndex = index;
        this.gestureStartX = event.clientX;
        this.gestureStartY = event.clientY;
        this.gestureVertexAbs = this.absShapePoints(item);
        window.addEventListener('pointermove', this.boundMove);
        window.addEventListener('pointerup', this.boundUp);
    }

    // Round a value to the layout grid (the spacing used by Show Grid).
    private snapToGrid(n: number): number {
        const g = this.layout.gridSize;
        return g > 0 ? Math.round(n / g) * g : Math.round(n);
    }

    // Grab an edge: insert a new corner at its midpoint and drag it out. The new
    // corner lands on the grid so it starts aligned like the others.
    onEdgeAdd(item: StreamItem, edgeIndex: number, event: PointerEvent): void {
        if (!this.layout.moveMode || event.button !== 0) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();
        const abs = this.absShapePoints(item);
        const a = abs[edgeIndex], b = abs[(edgeIndex + 1) % abs.length];
        abs.splice(edgeIndex + 1, 0, {
            x: Math.max(0, this.snapToGrid((a.x + b.x) / 2)),
            y: Math.max(0, this.snapToGrid((a.y + b.y) / 2)),
        });
        this.commitShapePoints(item.id, abs);
        const updated = this.layout.items.find((i) => i.id === item.id);
        this.gestureId = item.id;
        this.gestureMode = 'vertex';
        this.gestureVertexIndex = edgeIndex + 1;
        this.gestureStartX = event.clientX;
        this.gestureStartY = event.clientY;
        this.gestureVertexAbs = updated ? this.absShapePoints(updated) : abs;
        window.addEventListener('pointermove', this.boundMove);
        window.addEventListener('pointerup', this.boundUp);
    }

    // Double-click a corner to remove it (keep at least a triangle).
    onVertexDelete(item: StreamItem, index: number, event: Event): void {
        event.preventDefault();
        event.stopPropagation();
        const abs = this.absShapePoints(item);
        if (abs.length <= 3) {
            return;
        }
        abs.splice(index, 1);
        this.commitShapePoints(item.id, abs);
    }

    // Write absolute corner points back to a shape, re-anchoring x/y/w/h so the
    // item's box always tightly wraps the polygon (points stay >= 0).
    private commitShapePoints(id: string, abs: { x: number; y: number }[]): void {
        const xs = abs.map((p) => p.x), ys = abs.map((p) => p.y);
        const minX = Math.max(0, Math.min(...xs)), minY = Math.max(0, Math.min(...ys));
        const maxX = Math.max(...xs), maxY = Math.max(...ys);
        const points = abs.map((p) => ({ x: p.x - minX, y: p.y - minY }));
        this.streamLayoutService.updateItem(id, {
            x: minX, y: minY, w: Math.max(1, maxX - minX), h: Math.max(1, maxY - minY), points,
        });
    }

    // Add a divider (centred) to the right-clicked shapable border.
    addDivider(axis: 'h' | 'v'): void {
        if (!this.ctxItem || this.ctxItem.type !== 'shape') {
            return;
        }
        const dividers = [...(this.ctxItem.dividers ?? []), { axis, pos: 0.5 }];
        this.streamLayoutService.updateItem(this.ctxItem.id, { dividers });
        this.refreshCtxItem();
    }

    // Start dragging a divider along its axis.
    onDividerStart(item: StreamItem, index: number, event: PointerEvent): void {
        if (!this.layout.moveMode || event.button !== 0) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();
        this.gestureId = item.id;
        this.gestureMode = 'divider';
        this.gestureDividerIndex = index;
        this.gestureDividerStartPos = (item.dividers ?? [])[index]?.pos ?? 0.5;
        this.gestureStartX = event.clientX;
        this.gestureStartY = event.clientY;
        window.addEventListener('pointermove', this.boundMove);
        window.addEventListener('pointerup', this.boundUp);
    }

    // Double-click a divider to remove it.
    onDividerDelete(item: StreamItem, index: number, event: Event): void {
        event.preventDefault();
        event.stopPropagation();
        const dividers = (item.dividers ?? []).filter((_, i) => i !== index);
        this.streamLayoutService.updateItem(item.id, { dividers });
    }

    // SVG path for a closed polygon with rounded corners of a constant arc
    // radius. A true corner fillet is tangent to both edges, so how far back the
    // arc starts depends on the corner's angle: sharp corners cut back further,
    // near-straight corners barely round. The cut-back is clamped to half the
    // shorter adjacent edge (reducing the radius there if needed). The arc curves
    // the way the path turns, so it's correct for any winding.
    private roundedPath(poly: { x: number; y: number }[], radius: number): string {
        const n = poly.length;
        if (n < 3) { return ''; }
        const pin: { x: number; y: number }[] = [];
        const pout: { x: number; y: number }[] = [];
        const sweep: number[] = [];
        const arc: number[] = [];
        for (let i = 0; i < n; i++) {
            const prev = poly[(i - 1 + n) % n], v = poly[i], next = poly[(i + 1) % n];
            const inX = v.x - prev.x, inY = v.y - prev.y;
            const outX = next.x - v.x, outY = next.y - v.y;
            const lenIn = Math.hypot(inX, inY) || 1, lenOut = Math.hypot(outX, outY) || 1;
            const uinX = inX / lenIn, uinY = inY / lenIn;
            const uoutX = outX / lenOut, uoutY = outY / lenOut;
            const cross = uinX * uoutY - uinY * uoutX;   // sin(turn angle), signed
            const dot = uinX * uoutX + uinY * uoutY;      // cos(turn angle)
            const sin = Math.abs(cross);
            // tan(half the turn angle); near 0 on a straight run, large at a cusp
            const halfTan = sin / Math.max(1e-6, 1 + dot);
            // Straight point (collinear, not a cusp): no rounding.
            if (sin < 0.02 && dot > 0) {
                pin.push(v); pout.push(v); arc.push(0); sweep.push(0);
                continue;
            }
            // Tangent cut-back for this radius, clamped so the arc fits the edges;
            // the arc radius shrinks with it when clamped.
            let t = radius * halfTan;
            t = Math.min(t, lenIn / 2, lenOut / 2);
            const r = halfTan > 1e-6 ? t / halfTan : 0;
            pin.push({ x: v.x - uinX * t, y: v.y - uinY * t });
            pout.push({ x: v.x + uoutX * t, y: v.y + uoutY * t });
            arc.push(r);
            sweep.push(cross > 0 ? 1 : 0);
        }
        const f = (p: { x: number; y: number }) => `${p.x.toFixed(2)} ${p.y.toFixed(2)}`;
        let d = `M ${f(pout[0])}`;
        for (let step = 1; step <= n; step++) {
            const i = step % n;
            if (arc[i] > 0.5) {
                d += ` L ${f(pin[i])} A ${arc[i].toFixed(2)} ${arc[i].toFixed(2)} 0 0 ${sweep[i]} ${f(pout[i])}`;
            } else {
                // No rounding: go straight through the vertex. Emitting both pin
                // and pout (which coincide) would make a zero-length segment that
                // a round line-join renders as a stray disc on a flat edge.
                d += ` L ${f(poly[i])}`;
            }
        }
        return d + ' Z';
    }

    // True when nothing is playing and the queue is empty (idle). Drives the
    // per-element "hide while idle" toggles.
    get waitingForCall(): boolean {
        return !this.call && this.callQueue === 0;
    }

    // Whether the data value should be shown given the call/idle hide toggles.
    dataVisible(item: StreamItem): boolean {
        if (item.hideOnCall && !this.waitingForCall) {
            return false;
        }
        if (item.hideOnIdle && this.waitingForCall) {
            return false;
        }
        return true;
    }

    // Whether the title should be shown given its own call/idle hide toggles.
    titleVisible(item: StreamItem): boolean {
        if (item.titleHideOnCall && !this.waitingForCall) {
            return false;
        }
        if (item.titleHideOnIdle && this.waitingForCall) {
            return false;
        }
        return true;
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
        const mw = rect.width;
        const mh = rect.height;
        const vw = window.innerWidth;
        const vh = window.innerHeight;
        const margin = 6;

        let x = this.ctxX;
        let y = this.ctxY;

        // When an element was right-clicked, try to place the menu beside it so
        // it doesn't cover the element (which the user is editing). Only fall
        // back to over-the-cursor if no side fits.
        const root = this.streamRootRef?.nativeElement;
        if (this.ctxItem && root) {
            const r = root.getBoundingClientRect();
            const item = this.ctxItem;
            const eLeft = r.left + item.x;
            const eTop = r.top + item.y;
            const eRight = eLeft + item.w;
            const eBottom = eTop + item.h;
            const gap = 8;
            const clampX = (cx: number) => Math.max(margin, Math.min(cx, vw - mw - margin));
            const clampY = (cy: number) => Math.max(margin, Math.min(cy, vh - mh - margin));
            const ok = (cx: number, cy: number): boolean => {
                if (cx < margin || cy < margin || cx + mw > vw - margin || cy + mh > vh - margin) {
                    return false;
                }
                const overlaps = !(cx >= eRight || cx + mw <= eLeft || cy >= eBottom || cy + mh <= eTop);
                return !overlaps;
            };
            const candidates: Array<[number, number]> = [
                [eRight + gap, clampY(eTop)],       // right of element
                [eLeft - mw - gap, clampY(eTop)],   // left of element
                [clampX(eLeft), eBottom + gap],     // below element
                [clampX(eLeft), eTop - mh - gap],   // above element
            ];
            let placed = false;
            for (const [cx, cy] of candidates) {
                if (ok(cx, cy)) {
                    x = cx;
                    y = cy;
                    placed = true;
                    break;
                }
            }
            if (!placed) {
                x = clampX(this.ctxX);
                y = clampY(this.ctxY);
            }
        } else {
            if (rect.right > vw - margin) {
                x = Math.max(margin, vw - mw - margin);
            }
            if (rect.bottom > vh - margin) {
                y = Math.max(margin, vh - mh - margin);
            }
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
        // A new shapable border starts grid-aligned so its corners sit on the grid.
        const x = type === 'shape' ? this.snapToGrid(this.addX) : this.addX;
        const y = type === 'shape' ? this.snapToGrid(this.addY) : this.addY;
        this.streamLayoutService.addItem(type, x, y);
        this.closeContext();
    }

    private applyToTargets(patch: Partial<StreamItem>): void {
        for (const id of this.targetIds()) {
            this.streamLayoutService.updateItem(id, patch);
        }
    }

    // Styling fields carried by Copy/Paste — everything visual, but not the
    // element's identity, geometry, or content (id/type/x/y/w/h/text/title
    // text/history columns stay put).
    private static readonly STYLE_KEYS: (keyof StreamItem)[] = [
        'color', 'fontSize', 'fontFamily', 'bold', 'align', 'useLedColor',
        'hideOnCall', 'hideOnIdle', 'titleHideOnCall', 'titleHideOnIdle',
        'titleEnabled', 'titleColor', 'titleBold', 'titleUseLed', 'titleFontSize', 'titleFontFamily',
        'autoScroll', 'histRowLines', 'histColLines', 'histLineWidth', 'histLineColor',
        'borderWidth', 'innerWidth', 'cornerRadius', 'centerFill', 'centerColor', 'centerUseLed',
        'middleFill', 'middleWidth', 'middleColor', 'middleUseLed',
    ];

    copyItemStyle(): void {
        const src = this.ctxItem;
        if (!src) {
            return;
        }
        const style: Partial<StreamItem> = {};
        for (const k of RdioScannerStreamComponent.STYLE_KEYS) {
            (style as Record<string, unknown>)[k] = src[k];
        }
        this.copiedStyle = style;
        this.copiedStyleType = src.type;
        this.snack.open('Settings copied — right-click another element to paste', '', { duration: 2000 });
    }

    pasteItemStyle(): void {
        if (!this.copiedStyle) {
            return;
        }
        this.applyToTargets({ ...this.copiedStyle });
        const n = this.targetIds().length;
        this.snack.open(`Settings pasted${n > 1 ? ' to ' + n + ' elements' : ''}`, '', { duration: 1500 });
        this.refreshCtxItem();
    }

    removeCtxItem(): void {
        const ids = this.targetIds();
        this.closeContext();
        if (!ids.length) {
            return;
        }
        this.askConfirm(`Remove ${ids.length} element${ids.length > 1 ? 's' : ''}?`, () => {
            ids.forEach((id) => this.streamLayoutService.removeItem(id));
            this.selectedIds.clear();
        });
    }

    private askConfirm(message: string, action: () => void): void {
        this.confirm = { message, action };
        this.cdr.detectChanges();
    }

    confirmYes(): void {
        const action = this.confirm?.action;
        this.confirm = null;
        if (action) {
            action();
        }
        this.cdr.detectChanges();
    }

    confirmNo(): void {
        this.confirm = null;
        this.cdr.detectChanges();
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

    setAlign(align: 'left' | 'center' | 'right'): void {
        this.applyToTargets({ align });
    }

    setHideOnCall(hideOnCall: boolean): void {
        this.applyToTargets({ hideOnCall });
    }

    setHideOnIdle(hideOnIdle: boolean): void {
        this.applyToTargets({ hideOnIdle });
    }

    setTitleHideOnCall(titleHideOnCall: boolean): void {
        this.applyToTargets({ titleHideOnCall });
    }

    setTitleHideOnIdle(titleHideOnIdle: boolean): void {
        this.applyToTargets({ titleHideOnIdle });
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
        this.refreshCtxItem();
    }

    setCtxItemTitleText(titleText: string): void {
        // Per-item text — only the right-clicked item.
        if (this.ctxItem) {
            this.streamLayoutService.updateItem(this.ctxItem.id, { titleText });
        }
    }

    setCtxItemTitleColor(titleColor: string): void {
        this.applyToTargets({ titleColor });
    }

    setCtxItemTitleBold(titleBold: boolean): void {
        this.applyToTargets({ titleBold });
    }

    setCtxItemTitleUseLed(titleUseLed: boolean): void {
        this.applyToTargets({ titleUseLed });
    }

    setCtxItemTitleSize(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ titleFontSize: Math.max(6, Math.min(200, Math.round(value))) });
        }
    }

    setCtxItemTitleFont(titleFontFamily: string): void {
        this.applyToTargets({ titleFontFamily });
    }

    trackHistCol(_index: number, col: StreamHistoryCol): string {
        return col.key;
    }

    // Patch one column of the right-clicked history table. Reads the current
    // columns from the live layout (not the possibly-stale ctxItem) and does
    // NOT reassign ctxItem — otherwise the column *ngFor rebuilds its inputs
    // mid-edit and the native color picker closes.
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
        const current = this.layout.items.find((i) => i.id === id);
        const baseCols = current?.historyCols ?? this.ctxItem.historyCols ?? [];
        const cols = baseCols.map((c, i) => (i === index ? { ...c, ...patch } : c));
        this.streamLayoutService.updateItem(id, { historyCols: cols });
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
        this.closeContext();
        this.askConfirm('Reset the entire layout to defaults? This removes all your changes.', () => {
            this.streamLayoutService.reset();
        });
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

    // The item currently being resized (for the live dimension readout), or null.
    get resizingItem(): StreamItem | null {
        if (this.gestureMode !== 'resize' || !this.gestureId) {
            return null;
        }
        return this.layout.items.find((i) => i.id === this.gestureId) ?? null;
    }

    // The single item currently being moved (for the live position readout).
    get movingItem(): StreamItem | null {
        if (this.gestureMode !== 'move' || !this.gestureId || this.moveTargets.length !== 1) {
            return null;
        }
        return this.layout.items.find((i) => i.id === this.gestureId) ?? null;
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
            // Single-item drags snap to other elements' edges (alignment guides)
            // unless Shift is held (which forces grid snap instead).
            const elementSnap = this.moveTargets.length === 1 && !event.shiftKey;
            this.guideX = null;
            this.guideY = null;

            for (const t of this.moveTargets) {
                let x = Math.max(0, snap(t.x + dx));
                let y = Math.max(0, snap(t.y + dy));

                if (elementSnap) {
                    const item = this.layout.items.find((i) => i.id === t.id);
                    if (item) {
                        const s = this.snapToElements(x, y, item.w, item.h, t.id);
                        x = s.x;
                        y = s.y;
                        this.guideX = s.guideX;
                        this.guideY = s.guideY;
                    }
                }

                this.streamLayoutService.updateItem(t.id, { x, y });
            }
        } else if (this.gestureMode === 'vertex') {
            // Drag a single shapable-border corner; re-anchor the box to wrap it.
            // Corners snap to the grid by default (hold Shift for free placement).
            const vsnap = (n: number): number => (event.shiftKey ? Math.round(n) : this.snapToGrid(n));
            const abs = this.gestureVertexAbs.map((p) => ({ ...p }));
            const start = this.gestureVertexAbs[this.gestureVertexIndex];
            if (start) {
                abs[this.gestureVertexIndex] = {
                    x: Math.max(0, vsnap(start.x + dx)),
                    y: Math.max(0, vsnap(start.y + dy)),
                };
                this.commitShapePoints(this.gestureId, abs);
            }
        } else if (this.gestureMode === 'divider') {
            // Slide a divider along its axis (snaps to grid; Shift for free).
            const item = this.layout.items.find((i) => i.id === this.gestureId);
            const dividers = item?.dividers ? item.dividers.map((d) => ({ ...d })) : null;
            const dv = dividers?.[this.gestureDividerIndex];
            if (item && dividers && dv) {
                const gsnap = (n: number): number => (event.shiftKey ? Math.round(n) : this.snapToGrid(n));
                if (dv.axis === 'v' && item.w > 0) {
                    const abs = gsnap(item.x + this.gestureDividerStartPos * item.w + dx);
                    dv.pos = Math.max(0, Math.min(1, (abs - item.x) / item.w));
                } else if (dv.axis === 'h' && item.h > 0) {
                    const abs = gsnap(item.y + this.gestureDividerStartPos * item.h + dy);
                    dv.pos = Math.max(0, Math.min(1, (abs - item.y) / item.h));
                }
                this.streamLayoutService.updateItem(item.id, { dividers });
            }
        } else {
            const item = this.layout.items.find((i) => i.id === this.gestureId);
            const minW = item ? streamItemMinW(item.type) : 20;
            const minH = item ? streamItemMinH(item.type) : 16;
            const ix = item ? item.x : 0;
            const iy = item ? item.y : 0;
            // Snap the box's bottom-right EDGE to the grid (matching how move
            // snaps the top-left), so resized edges land on grid lines too.
            const w = Math.max(minW, snap(ix + this.gestureOrigW + dx) - ix);
            const h = Math.max(minH, snap(iy + this.gestureOrigH + dy) - iy);
            this.streamLayoutService.updateItem(this.gestureId, { w, h });
        }
    }

    private onGestureEnd(): void {
        this.gestureId = null;
        this.gestureMode = null;
        this.moveTargets = [];
        this.guideX = null;
        this.guideY = null;
        this.detachGestureListeners();
    }

    // Snap a dragged box's edges/center to nearby other elements' edges/center
    // so it lines up with borders / other readouts. Returns the adjusted
    // position and the guide lines (canvas-local) that were snapped to.
    private snapToElements(
        x: number, y: number, w: number, h: number, id: string,
    ): { x: number; y: number; guideX: number | null; guideY: number | null } {
        const threshold = 7;
        // Only align to elements that are actually near the dragged one in the
        // perpendicular axis (overlapping elements like frames have 0 gap and
        // always qualify), so it doesn't snap to things across the screen.
        const near = 130;
        // Text sits vertically centered in its box, so for non-frame items
        // prefer center-to-center alignment over edge alignment by discounting
        // its match distance.
        const dragged = this.layout.items.find((i) => i.id === id);
        const preferCenter = !!dragged && !streamIsBorder(dragged.type);
        const centerBonus = 5;
        const movingIds = new Set(this.moveTargets.map((t) => t.id));

        let bestScoreX = Infinity;
        let bestScoreY = Infinity;
        let snapX = x;
        let snapY = y;
        let guideX: number | null = null;
        let guideY: number | null = null;

        const myV = [x, x + w / 2, x + w];
        const myH = [y, y + h / 2, y + h];

        for (const o of this.layout.items) {
            if (o.id === id || movingIds.has(o.id)) {
                continue;
            }
            // Vertical / horizontal separation (0 when the boxes overlap).
            const vSep = Math.max(0, o.y - (y + h), y - (o.y + o.h));
            const hSep = Math.max(0, o.x - (x + w), x - (o.x + o.w));

            // Align vertical edges only when the two are vertically close.
            if (vSep <= near) {
                const oV = [o.x, o.x + o.w / 2, o.x + o.w];
                for (let mi = 0; mi < 3; mi++) {
                    for (let vi = 0; vi < 3; vi++) {
                        const d = Math.abs(myV[mi] - oV[vi]);
                        if (d > threshold) {
                            continue;
                        }
                        const score = d - (preferCenter && mi === 1 && vi === 1 ? centerBonus : 0);
                        if (score < bestScoreX) {
                            bestScoreX = score;
                            snapX = x + (oV[vi] - myV[mi]);
                            guideX = oV[vi];
                        }
                    }
                }
            }

            // Align horizontal edges only when horizontally close.
            if (hSep <= near) {
                const oH = [o.y, o.y + o.h / 2, o.y + o.h];
                for (let mi = 0; mi < 3; mi++) {
                    for (let vi = 0; vi < 3; vi++) {
                        const d = Math.abs(myH[mi] - oH[vi]);
                        if (d > threshold) {
                            continue;
                        }
                        const score = d - (preferCenter && mi === 1 && vi === 1 ? centerBonus : 0);
                        if (score < bestScoreY) {
                            bestScoreY = score;
                            snapY = y + (oH[vi] - myH[mi]);
                            guideY = oH[vi];
                        }
                    }
                }
            }
        }

        return { x: Math.max(0, snapX), y: Math.max(0, snapY), guideX, guideY };
    }

    setShowGrid(showGrid: boolean): void {
        this.streamLayoutService.update({ showGrid });
    }

    setHistRowLines(histRowLines: boolean): void {
        this.applyToTargets({ histRowLines });
    }

    setHistColLines(histColLines: boolean): void {
        this.applyToTargets({ histColLines });
    }

    setHistLineWidth(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ histLineWidth: Math.max(0, Math.min(20, value)) });
        }
    }

    setHistLineColor(histLineColor: string): void {
        this.applyToTargets({ histLineColor });
    }

    // Re-point ctxItem at the freshly-updated item so menu controls gated on a
    // just-toggled field (e.g. inner/middle band, title) show/hide live.
    private refreshCtxItem(): void {
        if (this.ctxItem) {
            const id = this.ctxItem.id;
            this.ctxItem = this.layout.items.find((i) => i.id === id) ?? this.ctxItem;
        }
    }

    private clampWidth(value: number): number {
        return Math.max(0, Math.min(40, Math.round(value)));
    }

    setBorderWidth(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ borderWidth: this.clampWidth(value) });
        }
    }

    setInnerWidth(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ innerWidth: this.clampWidth(value) });
        }
    }

    setCornerRadius(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ cornerRadius: Math.max(0, Math.min(200, Math.round(value))) });
        }
    }

    setCenterFill(centerFill: boolean): void {
        this.applyToTargets({ centerFill });
        this.refreshCtxItem();
    }

    setCenterColor(centerColor: string): void {
        this.applyToTargets({ centerColor });
    }

    setCenterUseLed(centerUseLed: boolean): void {
        this.applyToTargets({ centerUseLed });
    }

    setMiddleFill(middleFill: boolean): void {
        this.applyToTargets({ middleFill });
        this.refreshCtxItem();
    }

    setMiddleWidth(value: number): void {
        if (Number.isFinite(value)) {
            this.applyToTargets({ middleWidth: this.clampWidth(value) });
        }
    }

    setMiddleColor(middleColor: string): void {
        this.applyToTargets({ middleColor });
    }

    setMiddleUseLed(middleUseLed: boolean): void {
        this.applyToTargets({ middleUseLed });
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

            } else if (type !== 'history' && !streamIsBorder(type) && type !== 'text') {
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

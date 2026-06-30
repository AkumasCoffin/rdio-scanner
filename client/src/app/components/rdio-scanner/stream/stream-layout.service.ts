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

import { EventEmitter, Injectable, OnDestroy } from '@angular/core';
import {
    StreamHistoryCol,
    StreamItem,
    StreamLayout,
    STREAM_DEFAULT_BORDER_COLOR,
    STREAM_DEFAULT_TEXT_COLOR,
    STREAM_DEFAULT_TITLE_COLOR,
    STREAM_LAYOUT_CHANNEL,
    STREAM_LAYOUT_STORAGE_KEY,
    defaultHistoryCols,
    defaultStreamLayout,
    streamItemTypeDef,
} from './stream-layout';

// Shared, live-synced layout state for the /stream OBS overlay.
//
// The Stream-settings menu (on the main page) writes here; the /stream page
// reads here. The two run in separate browser windows, so changes are mirrored
// across windows via a BroadcastChannel and persisted to localStorage so a
// freshly-opened /stream window restores the last layout.
@Injectable({ providedIn: 'root' })
export class StreamLayoutService implements OnDestroy {
    // Fires whenever the layout changes — from a local edit OR a remote
    // (other-window) update. Components subscribe and re-render.
    changes = new EventEmitter<StreamLayout>();

    private layout: StreamLayout = this.load();

    private channel: BroadcastChannel | undefined;

    // Monotonic counter feeding unique item ids (combined with a timestamp).
    private idSeq = 0;

    constructor() {
        try {
            if (typeof BroadcastChannel !== 'undefined') {
                this.channel = new BroadcastChannel(STREAM_LAYOUT_CHANNEL);
                this.channel.onmessage = (e: MessageEvent) => this.onRemote(e?.data);
            }
        } catch (_) {
            // BroadcastChannel unavailable — degrade to localStorage only.
        }
    }

    ngOnDestroy(): void {
        try {
            this.channel?.close();
        } catch (_) {
            //
        }
    }

    getLayout(): StreamLayout {
        return this.layout;
    }

    // Patch top-level fields (bgColor / moveMode / gridSize).
    update(partial: Partial<Omit<StreamLayout, 'items'>>): void {
        this.layout = { ...this.layout, ...partial };
        this.commit(true);
    }

    // Add a new item of the given type at the given spot (defaults to 40,40).
    // Returns its id.
    addItem(type: string, x = 40, y = 40): string {
        const def = streamItemTypeDef(type);
        if (!def) {
            return '';
        }
        const id = this.genId();
        const item: StreamItem = {
            id,
            type,
            x: Math.max(0, Math.round(x)),
            y: Math.max(0, Math.round(y)),
            w: def.w,
            h: def.h,
            color: type === 'frame' ? STREAM_DEFAULT_BORDER_COLOR : STREAM_DEFAULT_TEXT_COLOR,
            fontSize: def.fontSize,
            fontFamily: '',
            bold: true,
            text: type === 'text' ? 'Text' : '',
            hideOnCall: false,
            hideOnIdle: false,
            titleHideOnCall: false,
            titleHideOnIdle: false,
            titleEnabled: def.titleOn,
            titleColor: STREAM_DEFAULT_TITLE_COLOR,
            titleBold: true,
            titleUseLed: false,
            titleFontSize: def.fontSize,
            titleFontFamily: '',
            titleText: '',
            useLedColor: false,
            align: 'left',
            autoScroll: true,
            historyCols: type === 'history' ? defaultHistoryCols() : [],
            histRowLines: true,
            histColLines: false,
            histLineWidth: 1,
            histLineColor: '#888888',
            borderWidth: 2,
            innerWidth: 2,
            cornerRadius: 6,
            centerFill: false,
            centerColor: '#000000',
            centerUseLed: false,
            middleFill: false,
            middleWidth: 2,
            middleColor: '#888888',
            middleUseLed: false,
            linkMode: false,
            linkDivider: false,
        };
        this.layout = { ...this.layout, items: [...this.layout.items, item] };
        this.commit(true);
        return id;
    }

    removeItem(id: string): void {
        this.layout = { ...this.layout, items: this.layout.items.filter((i) => i.id !== id) };
        this.commit(true);
    }

    updateItem(id: string, partial: Partial<StreamItem>): void {
        this.layout = {
            ...this.layout,
            items: this.layout.items.map((i) => (i.id === id ? { ...i, ...partial } : i)),
        };
        this.commit(true);
    }

    // Restore everything to the built-in defaults.
    reset(): void {
        this.layout = defaultStreamLayout();
        this.commit(true);
    }

    // Serialize the current layout for download / sharing.
    exportLayout(): string {
        return JSON.stringify(this.layout, null, 2);
    }

    // Load a layout from an exported JSON string. Returns a result so the UI
    // can report success/failure. Unknown/old shapes are normalized.
    importLayout(json: string): { success: boolean; error?: string } {
        let parsed: unknown;
        try {
            parsed = JSON.parse(json);
        } catch (_) {
            return { success: false, error: 'Not valid JSON.' };
        }

        if (!parsed || typeof parsed !== 'object') {
            return { success: false, error: 'Not a valid stream layout.' };
        }

        this.layout = this.normalize(parsed as Partial<StreamLayout>);
        this.commit(true);
        return { success: true };
    }

    private genId(): string {
        // App-side id (Date.now/Math.random are fine here — this isn't a
        // workflow script). Combined with a counter to avoid collisions within
        // the same millisecond.
        this.idSeq += 1;
        return `i${Date.now().toString(36)}-${this.idSeq.toString(36)}-${Math.floor(Math.random() * 1e6).toString(36)}`;
    }

    private onRemote(data: unknown): void {
        if (!data || typeof data !== 'object') {
            return;
        }
        this.layout = this.normalize(data as Partial<StreamLayout>);
        // Apply + persist locally, but don't re-broadcast (avoids ping-pong).
        this.commit(false);
    }

    // Persist, optionally broadcast, then notify local subscribers.
    private commit(broadcast: boolean): void {
        try {
            window?.localStorage?.setItem(STREAM_LAYOUT_STORAGE_KEY, JSON.stringify(this.layout));
        } catch (_) {
            //
        }

        if (broadcast) {
            try {
                this.channel?.postMessage(this.layout);
            } catch (_) {
                //
            }
        }

        this.changes.emit(this.layout);
    }

    private load(): StreamLayout {
        try {
            const raw = window?.localStorage?.getItem(STREAM_LAYOUT_STORAGE_KEY);
            if (raw) {
                return this.normalize(JSON.parse(raw));
            }
        } catch (_) {
            //
        }
        return defaultStreamLayout();
    }

    // Coerce arbitrary stored/received data into a valid layout. Falls back to
    // defaults for anything missing or malformed; drops items of unknown type.
    private normalize(input: Partial<StreamLayout> | null | undefined): StreamLayout {
        const base = defaultStreamLayout();
        if (!input || typeof input !== 'object') {
            return base;
        }

        const items = Array.isArray(input.items)
            ? input.items.reduce((acc: StreamItem[], raw) => {
                const item = this.normalizeItem(raw);
                if (item) {
                    acc.push(item);
                }
                return acc;
            }, [])
            : base.items;

        return {
            bgColor: typeof input.bgColor === 'string' ? input.bgColor : base.bgColor,
            // moveMode never persists across a fresh load as "on" by accident —
            // but we honour whatever was stored so a live toggle survives sync.
            moveMode: typeof input.moveMode === 'boolean' ? input.moveMode : base.moveMode,
            gridSize: typeof input.gridSize === 'number' ? input.gridSize : base.gridSize,
            showGrid: typeof input.showGrid === 'boolean' ? input.showGrid : base.showGrid,
            items,
        };
    }

    private normalizeItem(raw: unknown): StreamItem | null {
        if (!raw || typeof raw !== 'object') {
            return null;
        }
        const r = raw as Partial<StreamItem>;
        const def = typeof r.type === 'string' ? streamItemTypeDef(r.type) : undefined;
        if (!def) {
            return null;
        }
        const isFrame = def.type === 'frame';
        return {
            id: typeof r.id === 'string' && r.id ? r.id : this.genId(),
            type: def.type,
            x: typeof r.x === 'number' ? r.x : 40,
            y: typeof r.y === 'number' ? r.y : 40,
            w: typeof r.w === 'number' ? r.w : def.w,
            h: typeof r.h === 'number' ? r.h : def.h,
            color: typeof r.color === 'string'
                ? r.color
                : (isFrame ? STREAM_DEFAULT_BORDER_COLOR : STREAM_DEFAULT_TEXT_COLOR),
            fontSize: typeof r.fontSize === 'number' ? r.fontSize : def.fontSize,
            fontFamily: typeof r.fontFamily === 'string' ? r.fontFamily : '',
            bold: typeof r.bold === 'boolean' ? r.bold : true,
            text: typeof r.text === 'string' ? r.text : '',
            hideOnCall: typeof r.hideOnCall === 'boolean' ? r.hideOnCall : false,
            hideOnIdle: typeof r.hideOnIdle === 'boolean' ? r.hideOnIdle : false,
            titleHideOnCall: typeof r.titleHideOnCall === 'boolean' ? r.titleHideOnCall : false,
            titleHideOnIdle: typeof r.titleHideOnIdle === 'boolean' ? r.titleHideOnIdle : false,
            titleEnabled: typeof r.titleEnabled === 'boolean' ? r.titleEnabled : def.titleOn,
            titleColor: typeof r.titleColor === 'string' ? r.titleColor : STREAM_DEFAULT_TITLE_COLOR,
            titleBold: typeof r.titleBold === 'boolean' ? r.titleBold : true,
            titleUseLed: typeof r.titleUseLed === 'boolean' ? r.titleUseLed : false,
            titleFontSize: typeof r.titleFontSize === 'number' ? r.titleFontSize : def.fontSize,
            titleFontFamily: typeof r.titleFontFamily === 'string' ? r.titleFontFamily : '',
            titleText: typeof r.titleText === 'string' ? r.titleText : '',
            useLedColor: typeof r.useLedColor === 'boolean' ? r.useLedColor : false,
            align: r.align === 'center' || r.align === 'right' ? r.align : 'left',
            autoScroll: typeof r.autoScroll === 'boolean' ? r.autoScroll : true,
            historyCols: def.type === 'history' ? this.normalizeHistoryCols(r.historyCols) : [],
            histRowLines: typeof r.histRowLines === 'boolean' ? r.histRowLines : true,
            histColLines: typeof r.histColLines === 'boolean' ? r.histColLines : false,
            histLineWidth: typeof r.histLineWidth === 'number' ? r.histLineWidth : 1,
            histLineColor: typeof r.histLineColor === 'string' ? r.histLineColor : '#888888',
            borderWidth: typeof r.borderWidth === 'number' ? r.borderWidth : 2,
            innerWidth: typeof r.innerWidth === 'number' ? r.innerWidth : 2,
            cornerRadius: typeof r.cornerRadius === 'number' ? r.cornerRadius : 6,
            centerFill: typeof r.centerFill === 'boolean' ? r.centerFill : false,
            centerColor: typeof r.centerColor === 'string' ? r.centerColor : '#000000',
            centerUseLed: typeof r.centerUseLed === 'boolean' ? r.centerUseLed : false,
            middleFill: typeof r.middleFill === 'boolean' ? r.middleFill : false,
            middleWidth: typeof r.middleWidth === 'number' ? r.middleWidth : 2,
            middleColor: typeof r.middleColor === 'string' ? r.middleColor : '#888888',
            middleUseLed: typeof r.middleUseLed === 'boolean' ? r.middleUseLed : false,
            linkMode: typeof r.linkMode === 'boolean' ? r.linkMode : false,
            linkDivider: typeof r.linkDivider === 'boolean' ? r.linkDivider : false,
        };
    }

    private normalizeHistoryCols(input: unknown): StreamHistoryCol[] {
        const base = defaultHistoryCols();
        if (!Array.isArray(input)) {
            return base;
        }
        return base.map((bc) => {
            const found = (input as Partial<StreamHistoryCol>[]).find((c) => c && c.key === bc.key);
            if (!found) {
                return bc;
            }
            return {
                key: bc.key,
                title: typeof found.title === 'string' ? found.title : bc.title,
                visible: typeof found.visible === 'boolean' ? found.visible : bc.visible,
                color: typeof found.color === 'string' ? found.color : bc.color,
                fontSize: typeof found.fontSize === 'number' ? found.fontSize : bc.fontSize,
                bold: typeof found.bold === 'boolean' ? found.bold : bc.bold,
            };
        });
    }
}

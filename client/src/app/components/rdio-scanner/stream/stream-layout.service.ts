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
    StreamElementLayout,
    StreamLayout,
    STREAM_ELEMENTS,
    STREAM_LAYOUT_CHANNEL,
    STREAM_LAYOUT_STORAGE_KEY,
    defaultStreamLayout,
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

    // Patch top-level fields (colors / moveMode / gridSize).
    update(partial: Partial<Omit<StreamLayout, 'elements'>>): void {
        this.layout = { ...this.layout, ...partial };
        this.commit(true);
    }

    // Patch a single element's visibility and/or position.
    updateElement(key: string, partial: Partial<StreamElementLayout>): void {
        const current = this.layout.elements[key] ?? { visible: true, x: 0, y: 0 };
        this.layout = {
            ...this.layout,
            elements: { ...this.layout.elements, [key]: { ...current, ...partial } },
        };
        this.commit(true);
    }

    setVisible(key: string, visible: boolean): void {
        this.updateElement(key, { visible });
    }

    setPosition(key: string, x: number, y: number): void {
        this.updateElement(key, { x, y });
    }

    // Restore everything to the built-in defaults.
    reset(): void {
        this.layout = defaultStreamLayout();
        this.commit(true);
    }

    private onRemote(data: unknown): void {
        if (!data || typeof data !== 'object') {
            return;
        }
        this.layout = this.normalize(data as StreamLayout);
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

    // Merge a stored/received layout over the defaults so new element keys
    // (added in a later version) always exist and bad data can't break the UI.
    private normalize(input: Partial<StreamLayout> | null | undefined): StreamLayout {
        const base = defaultStreamLayout();
        if (!input || typeof input !== 'object') {
            return base;
        }

        const elements = { ...base.elements };
        const inEls = input.elements || {};
        for (const { key } of STREAM_ELEMENTS) {
            const el = inEls[key];
            if (el && typeof el === 'object') {
                elements[key] = {
                    visible: typeof el.visible === 'boolean' ? el.visible : base.elements[key].visible,
                    x: typeof el.x === 'number' ? el.x : base.elements[key].x,
                    y: typeof el.y === 'number' ? el.y : base.elements[key].y,
                };
            }
        }

        return {
            textColor: typeof input.textColor === 'string' ? input.textColor : base.textColor,
            bgColor: typeof input.bgColor === 'string' ? input.bgColor : base.bgColor,
            moveMode: typeof input.moveMode === 'boolean' ? input.moveMode : base.moveMode,
            gridSize: typeof input.gridSize === 'number' ? input.gridSize : base.gridSize,
            elements,
        };
    }
}

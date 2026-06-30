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

// The /stream OBS overlay is an instance-based canvas: users add as many items
// as they like (including multiples of the same type and multiple border
// frames), position/size/recolor each, and remove the ones they don't want.
//
// A StreamItem is one placed thing. `type` selects what it renders (a readout,
// a flag, the transcript, or a border frame). `color` is the text color, or the
// border color for a frame.
export interface StreamItem {
    id: string;
    type: string;
    x: number;
    y: number;
    w: number;
    h: number;
    color: string;
    // Text size (px) and font family. Empty fontFamily inherits the page's
    // default monospace font. Ignored for the 'frame' type.
    fontSize: number;
    fontFamily: string;
}

export interface StreamLayout {
    // Background color — default black so a white-text overlay reads well and
    // the background can be chroma-keyed out in OBS. When bgEnabled is false the
    // page is fully transparent instead (OBS can key on alpha directly).
    bgColor: string;
    bgEnabled: boolean;
    // When true, the /stream page is in edit mode: items are drag-movable and
    // resizable and right-click opens the editing context menu. Hold Shift
    // while dragging to snap to the grid.
    moveMode: boolean;
    // Grid size (px) used for Shift-drag snapping.
    gridSize: number;
    items: StreamItem[];
}

export const STREAM_DEFAULT_TEXT_COLOR = '#ffffff';
export const STREAM_DEFAULT_BORDER_COLOR = '#ffffff';

// Catalog of addable item types: a human label + the default box size used when
// a new instance is added. 'frame' is the border-box type.
export interface StreamItemType {
    type: string;
    label: string;
    w: number;
    h: number;
    fontSize: number;
}

export const STREAM_ITEM_TYPES: ReadonlyArray<StreamItemType> = [
    { type: 'clock', label: 'Time', w: 130, h: 28, fontSize: 18 },
    { type: 'callProgress', label: 'Call Time', w: 180, h: 28, fontSize: 18 },
    { type: 'listeners', label: 'Listeners', w: 150, h: 28, fontSize: 18 },
    { type: 'queue', label: 'Queue', w: 120, h: 28, fontSize: 18 },
    { type: 'delay', label: 'Delay', w: 150, h: 24, fontSize: 14 },
    { type: 'system', label: 'System', w: 220, h: 28, fontSize: 18 },
    { type: 'tag', label: 'Tag', w: 200, h: 28, fontSize: 18 },
    { type: 'talkgroup', label: 'Talkgroup', w: 220, h: 28, fontSize: 18 },
    { type: 'callDate', label: 'Call Date', w: 100, h: 28, fontSize: 18 },
    { type: 'talkgroupName', label: 'Talkgroup Name', w: 460, h: 46, fontSize: 32 },
    { type: 'tgid', label: 'TGID', w: 170, h: 28, fontSize: 18 },
    { type: 'uid', label: 'UID', w: 200, h: 28, fontSize: 18 },
    { type: 'tempAvoid', label: 'Avoid Timer', w: 100, h: 24, fontSize: 14 },
    { type: 'avoid', label: 'Avoid Flag', w: 90, h: 24, fontSize: 14 },
    { type: 'patch', label: 'Patch Flag', w: 90, h: 24, fontSize: 14 },
    { type: 'transcript', label: 'Transcript', w: 600, h: 170, fontSize: 20 },
    { type: 'frame', label: 'Border Frame', w: 560, h: 240, fontSize: 18 },
];

// Font choices offered in the context menu. '' = the page default (monospace).
export const STREAM_FONTS: ReadonlyArray<{ value: string; label: string }> = [
    { value: '', label: 'Default (mono)' },
    { value: 'Roboto, sans-serif', label: 'Roboto' },
    { value: 'Arial, sans-serif', label: 'Arial' },
    { value: 'Verdana, sans-serif', label: 'Verdana' },
    { value: 'Tahoma, sans-serif', label: 'Tahoma' },
    { value: '"Trebuchet MS", sans-serif', label: 'Trebuchet MS' },
    { value: 'Impact, sans-serif', label: 'Impact' },
    { value: 'Georgia, serif', label: 'Georgia' },
    { value: '"Times New Roman", serif', label: 'Times New Roman' },
    { value: '"Courier New", monospace', label: 'Courier New' },
    { value: 'Consolas, monospace', label: 'Consolas' },
];

export function streamItemTypeDef(type: string): StreamItemType | undefined {
    return STREAM_ITEM_TYPES.find((t) => t.type === type);
}

export function streamItemLabel(type: string): string {
    return streamItemTypeDef(type)?.label ?? type;
}

export const STREAM_LAYOUT_STORAGE_KEY = 'rdio-scanner-stream-layout';
export const STREAM_LAYOUT_CHANNEL = 'rdio-scanner-stream-layout';

// Out-of-the-box layout: an LCD frame + transcript frame, with the readouts
// arranged to mirror the main page's LCD (Call Time under the clock), and the
// transcript spaced below. Stable ids so resets are deterministic.
export function defaultStreamLayout(): StreamLayout {
    const frame = (id: string, x: number, y: number, w: number, h: number): StreamItem =>
        ({ id, type: 'frame', x, y, w, h, color: STREAM_DEFAULT_BORDER_COLOR, fontSize: 18, fontFamily: '' });

    const el = (type: string, x: number, y: number, w: number, h: number): StreamItem =>
        ({
            id: `default-${type}`, type, x, y, w, h,
            color: STREAM_DEFAULT_TEXT_COLOR,
            fontSize: streamItemTypeDef(type)?.fontSize ?? 18,
            fontFamily: '',
        });

    return {
        bgColor: '#000000',
        bgEnabled: true,
        moveMode: false,
        gridSize: 20,
        items: [
            // Frames first so they render behind the readouts.
            frame('default-lcd-frame', 12, 12, 560, 272),
            frame('default-transcript-frame', 12, 292, 624, 196),
            // Readouts.
            el('clock', 24, 24, 130, 28),
            el('callProgress', 24, 56, 180, 28),
            el('listeners', 214, 24, 150, 28),
            el('queue', 410, 24, 120, 28),
            el('delay', 410, 56, 150, 24),
            el('system', 24, 92, 220, 28),
            el('tag', 360, 92, 200, 28),
            el('talkgroup', 24, 124, 220, 28),
            el('callDate', 360, 124, 100, 28),
            el('talkgroupName', 24, 158, 460, 46),
            el('tgid', 24, 212, 170, 28),
            el('uid', 360, 212, 200, 28),
            el('tempAvoid', 24, 248, 100, 24),
            el('avoid', 134, 248, 90, 24),
            el('patch', 234, 248, 90, 24),
            el('transcript', 24, 306, 600, 168),
        ],
    };
}

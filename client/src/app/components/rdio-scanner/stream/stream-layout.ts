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

// Per-element layout for the /stream OBS overlay. Every LCD element is
// individually positioned (absolute x/y), toggleable (visible), and
// drag-movable in "move mode".
export interface StreamElementLayout {
    visible: boolean;
    x: number;
    y: number;
}

export interface StreamLayout {
    // Foreground (text) + background colors. Default is black-on-white so the
    // background can be chroma-keyed out in OBS.
    textColor: string;
    bgColor: string;
    // When true, the /stream page makes every element drag-movable. Hold Shift
    // while dragging to snap to the grid.
    moveMode: boolean;
    // Grid size (px) used for Shift-drag snapping.
    gridSize: number;
    elements: { [key: string]: StreamElementLayout };
}

// Ordered list of every movable/toggleable element on the stream LCD, with a
// human label (used by the settings menu) and its default position.
export const STREAM_ELEMENTS: ReadonlyArray<{ key: string; label: string; x: number; y: number }> = [
    { key: 'clock', label: 'Clock', x: 24, y: 16 },
    { key: 'listeners', label: 'Listeners', x: 220, y: 16 },
    { key: 'queue', label: 'Queue', x: 420, y: 16 },
    { key: 'delay', label: 'Delay', x: 420, y: 44 },
    { key: 'system', label: 'System', x: 24, y: 78 },
    { key: 'tag', label: 'Tag', x: 320, y: 78 },
    { key: 'talkgroup', label: 'Talkgroup', x: 24, y: 108 },
    { key: 'callDate', label: 'Call Date', x: 320, y: 108 },
    { key: 'callProgress', label: 'Call Time', x: 420, y: 108 },
    { key: 'talkgroupName', label: 'Talkgroup Name', x: 24, y: 142 },
    { key: 'tgid', label: 'TGID', x: 24, y: 188 },
    { key: 'uid', label: 'UID', x: 320, y: 188 },
    { key: 'tempAvoid', label: 'Avoid Timer', x: 24, y: 220 },
    { key: 'avoid', label: 'Avoid Flag', x: 150, y: 220 },
    { key: 'patch', label: 'Patch Flag', x: 250, y: 220 },
    { key: 'transcript', label: 'Transcript', x: 24, y: 276 },
];

export const STREAM_LAYOUT_STORAGE_KEY = 'rdio-scanner-stream-layout';
export const STREAM_LAYOUT_CHANNEL = 'rdio-scanner-stream-layout';

export function defaultStreamLayout(): StreamLayout {
    return {
        textColor: '#000000',
        bgColor: '#ffffff',
        moveMode: false,
        gridSize: 20,
        elements: STREAM_ELEMENTS.reduce((acc, el) => {
            acc[el.key] = { visible: true, x: el.x, y: el.y };
            return acc;
        }, {} as { [key: string]: StreamElementLayout }),
    };
}

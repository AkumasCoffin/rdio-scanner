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
    // Text size (px), font family and bold. Empty fontFamily inherits the
    // page's default monospace font. Ignored for the 'frame' type.
    fontSize: number;
    fontFamily: string;
    bold: boolean;
    // Free text for the 'text' (custom text box) type; unused otherwise.
    text: string;
    // Visibility by call state — independently for the data value and the title.
    // hideOnCall hides while a call is playing; hideOnIdle hides while nothing
    // is playing (idle). Both off = always shown.
    hideOnCall: boolean;
    hideOnIdle: boolean;
    titleHideOnCall: boolean;
    titleHideOnIdle: boolean;
    // Optional title/label shown before the value (e.g. "System: ...") with its
    // own fully-independent styling. Unsupported for flags, frames, custom text.
    titleEnabled: boolean;
    titleColor: string;
    titleBold: boolean;
    titleUseLed: boolean;
    titleFontSize: number;
    titleFontFamily: string;
    // When true the element's color follows the playing talkgroup's LCD (LED)
    // color instead of `color`.
    useLedColor: boolean;
    // Horizontal text alignment within the box.
    align: 'left' | 'center' | 'right';
    // When true, content that doesn't fit the box scrolls into view (single-line
    // values marquee horizontally; the transcript scrolls down in time with the
    // call) instead of being clipped.
    autoScroll: boolean;
    // Column config for the 'history' (playing history table) type; [] otherwise.
    historyCols: StreamHistoryCol[];
    // History table dividing lines (history type only).
    histRowLines: boolean;
    histColLines: boolean;
    histLineWidth: number;
    histLineColor: string;
    // Border frame (frame type only): outline width + inner band width, an
    // optional inner band, and corner rounding. The outline color is the
    // item's `color`; the inner band color is `centerColor`.
    borderWidth: number;
    innerWidth: number;
    cornerRadius: number;
    centerFill: boolean;
    centerColor: string;
    // Inner border colour follows the playing talkgroup's LCD/LED colour.
    centerUseLed: boolean;
    // Optional middle band between the outline and the inner band.
    middleFill: boolean;
    middleWidth: number;
    middleColor: string;
    middleUseLed: boolean;
}

// One column of the history table — toggleable, retitleable, with its own text
// settings.
export interface StreamHistoryCol {
    key: string;
    title: string;
    visible: boolean;
    color: string;
    fontSize: number;
    bold: boolean;
}

export const STREAM_HISTORY_COLS: ReadonlyArray<{ key: string; title: string }> = [
    { key: 'time', title: 'Time' },
    { key: 'system', title: 'System' },
    { key: 'talkgroup', title: 'Talkgroup' },
    { key: 'name', title: 'Name' },
];

export function defaultHistoryCols(): StreamHistoryCol[] {
    return STREAM_HISTORY_COLS.map((c) => ({
        key: c.key,
        title: c.title,
        visible: true,
        color: STREAM_DEFAULT_TEXT_COLOR,
        fontSize: 13,
        bold: false,
    }));
}

export interface StreamLayout {
    // Background color — default black so a white-text overlay reads well and
    // the background can be chroma-keyed out in OBS.
    bgColor: string;
    // When true, the /stream page is in edit mode: items are drag-movable and
    // resizable and right-click opens the editing context menu. Hold Shift
    // while dragging to snap to the grid.
    moveMode: boolean;
    // Grid size (px) used for Shift-drag snapping.
    gridSize: number;
    // Show the snap grid overlay while editing.
    showGrid: boolean;
    items: StreamItem[];
}

export const STREAM_DEFAULT_TEXT_COLOR = '#ffffff';
export const STREAM_DEFAULT_BORDER_COLOR = '#ffffff';
export const STREAM_DEFAULT_TITLE_COLOR = '#ffffff';

// Catalog of addable item types: a human label + default box size + default
// font size. `title` is the label shown before the value when titles are on
// ('' = the type has no title option: flags, frames, custom text, transcript).
// `titleOn` is the per-type default for the title toggle. 'frame' is the
// border-box type.
export interface StreamItemType {
    type: string;
    label: string;
    w: number;
    h: number;
    // Smallest the box may be resized to, so text isn't squeezed to nothing.
    minW: number;
    minH: number;
    fontSize: number;
    title: string;
    titleOn: boolean;
}

export const STREAM_ITEM_TYPES: ReadonlyArray<StreamItemType> = [
    { type: 'text', label: 'Custom Text', w: 200, h: 32, minW: 60, minH: 24, fontSize: 18, title: '', titleOn: false },
    { type: 'clock', label: 'Time', w: 140, h: 30, minW: 100, minH: 24, fontSize: 18, title: 'Time', titleOn: true },
    { type: 'callProgress', label: 'Call Time', w: 190, h: 30, minW: 120, minH: 24, fontSize: 18, title: 'Call Time', titleOn: true },
    { type: 'listeners', label: 'Listeners', w: 160, h: 30, minW: 110, minH: 24, fontSize: 18, title: 'Listeners', titleOn: true },
    { type: 'queue', label: 'Queue', w: 130, h: 30, minW: 90, minH: 24, fontSize: 18, title: 'Queue', titleOn: true },
    { type: 'delay', label: 'Delay', w: 160, h: 26, minW: 100, minH: 20, fontSize: 14, title: 'Delay', titleOn: true },
    { type: 'system', label: 'System', w: 280, h: 30, minW: 140, minH: 24, fontSize: 18, title: 'System', titleOn: false },
    { type: 'tag', label: 'Tag', w: 240, h: 30, minW: 120, minH: 24, fontSize: 18, title: 'Tag', titleOn: false },
    { type: 'talkgroup', label: 'Talkgroup', w: 280, h: 30, minW: 140, minH: 24, fontSize: 18, title: 'Talkgroup', titleOn: false },
    { type: 'callDate', label: 'Call Date', w: 110, h: 30, minW: 80, minH: 24, fontSize: 18, title: 'Date', titleOn: false },
    { type: 'talkgroupName', label: 'Talkgroup Name', w: 600, h: 44, minW: 200, minH: 30, fontSize: 26, title: 'Name', titleOn: false },
    { type: 'tgid', label: 'TGID', w: 180, h: 30, minW: 110, minH: 24, fontSize: 18, title: 'TGID', titleOn: true },
    { type: 'uid', label: 'UID', w: 320, h: 30, minW: 140, minH: 24, fontSize: 18, title: 'UID', titleOn: true },
    { type: 'tempAvoid', label: 'Avoid Timer', w: 110, h: 26, minW: 70, minH: 20, fontSize: 14, title: 'Avoid', titleOn: false },
    { type: 'avoid', label: 'Avoid Flag', w: 90, h: 26, minW: 60, minH: 20, fontSize: 14, title: '', titleOn: false },
    { type: 'patch', label: 'Patch Flag', w: 90, h: 26, minW: 60, minH: 20, fontSize: 14, title: '', titleOn: false },
    { type: 'transcript', label: 'Transcript', w: 600, h: 170, minW: 200, minH: 60, fontSize: 20, title: 'TRANSCRIPT', titleOn: true },
    { type: 'history', label: 'History Table', w: 600, h: 200, minW: 240, minH: 80, fontSize: 13, title: '', titleOn: false },
    { type: 'frame', label: 'Border Frame', w: 560, h: 240, minW: 40, minH: 30, fontSize: 18, title: '', titleOn: false },
];

export function streamItemMinW(type: string): number {
    return streamItemTypeDef(type)?.minW ?? 20;
}

export function streamItemMinH(type: string): number {
    return streamItemTypeDef(type)?.minH ?? 16;
}

export function streamItemTitle(type: string): string {
    return streamItemTypeDef(type)?.title ?? '';
}

// Font choices offered in the context menu. '' = the page default (monospace).
// The "radio / display" group are Google Fonts loaded only on the /stream page
// (they fall back to a generic family if offline).
export const STREAM_FONTS: ReadonlyArray<{ value: string; label: string }> = [
    { value: '', label: 'Default (mono)' },
    // Cool radio / scanner display fonts.
    { value: '"Orbitron", sans-serif', label: '★ Orbitron' },
    { value: '"Audiowide", sans-serif', label: '★ Audiowide' },
    { value: '"Share Tech Mono", monospace', label: '★ Share Tech Mono' },
    { value: '"VT323", monospace', label: '★ VT323 (CRT)' },
    { value: '"Wallpoet", sans-serif', label: '★ Wallpoet (LED)' },
    { value: '"Major Mono Display", monospace', label: '★ Major Mono' },
    { value: '"Chakra Petch", sans-serif', label: '★ Chakra Petch' },
    { value: '"Teko", sans-serif', label: '★ Teko' },
    { value: '"Bitcount Grid Double", monospace', label: '★ Bitcount Grid Double' },
    { value: '"Bitcount Single", monospace', label: '★ Bitcount Single' },
    { value: '"Pixelify Sans", sans-serif', label: '★ Pixelify Sans' },
    { value: '"Tourney", sans-serif', label: '★ Tourney' },
    // Standard fonts.
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

// Google Fonts stylesheet URL for the radio/display fonts above. Injected only
// while the /stream page is open.
export const STREAM_FONTS_HREF =
    'https://fonts.googleapis.com/css2?family=Orbitron:wght@400;700&family=Audiowide&family=Share+Tech+Mono&family=VT323&family=Wallpoet&family=Major+Mono+Display&family=Chakra+Petch:wght@400;700&family=Teko:wght@400;700&family=Bitcount+Grid+Double:wght@100..900&family=Bitcount+Single:wght@100..900&family=Pixelify+Sans:wght@400..700&family=Tourney:ital,wght@0,100..900;1,100..900&display=swap';

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
        ({
            id, type: 'frame', x, y, w, h, color: STREAM_DEFAULT_BORDER_COLOR,
            fontSize: 18, fontFamily: '', bold: true, text: '',
            hideOnCall: false, hideOnIdle: false, titleHideOnCall: false, titleHideOnIdle: false,
            titleEnabled: false, titleColor: STREAM_DEFAULT_TITLE_COLOR, titleBold: true,
            titleUseLed: false, titleFontSize: 18, titleFontFamily: '',
            useLedColor: false, align: 'left', autoScroll: true, historyCols: [],
            histRowLines: true, histColLines: false, histLineWidth: 1, histLineColor: '#888888',
            borderWidth: 2, innerWidth: 2, cornerRadius: 6, centerFill: false, centerColor: '#000000', centerUseLed: false,
            middleFill: false, middleWidth: 2, middleColor: '#888888', middleUseLed: false,
        });

    const el = (type: string, x: number, y: number, w: number, h: number): StreamItem =>
        ({
            id: `default-${type}`, type, x, y, w, h,
            color: STREAM_DEFAULT_TEXT_COLOR,
            fontSize: streamItemTypeDef(type)?.fontSize ?? 18,
            fontFamily: '',
            bold: true,
            text: '',
            hideOnCall: false,
            hideOnIdle: false,
            titleHideOnCall: false,
            titleHideOnIdle: false,
            titleEnabled: streamItemTypeDef(type)?.titleOn ?? false,
            titleColor: STREAM_DEFAULT_TITLE_COLOR,
            titleBold: true,
            titleUseLed: false,
            titleFontSize: streamItemTypeDef(type)?.fontSize ?? 18,
            titleFontFamily: '',
            useLedColor: false,
            align: 'left',
            autoScroll: true,
            historyCols: [],
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
        });

    return {
        bgColor: '#000000',
        moveMode: false,
        gridSize: 20,
        showGrid: true,
        items: [
            // Frames first so they render behind the readouts.
            frame('default-lcd-frame', 12, 12, 628, 292),
            frame('default-transcript-frame', 12, 308, 628, 184),
            // Readouts.
            el('clock', 24, 24, 140, 30),
            el('listeners', 200, 24, 160, 30),
            el('queue', 384, 24, 130, 30),
            el('callProgress', 24, 58, 190, 30),
            el('delay', 384, 58, 160, 26),
            el('system', 24, 96, 280, 30),
            el('tag', 330, 96, 240, 30),
            el('talkgroup', 24, 132, 280, 30),
            el('callDate', 330, 132, 110, 30),
            el('talkgroupName', 24, 170, 600, 44),
            el('tgid', 24, 226, 180, 30),
            el('uid', 300, 226, 320, 30),
            el('tempAvoid', 24, 266, 110, 26),
            el('avoid', 150, 266, 90, 26),
            el('patch', 250, 266, 90, 26),
            el('transcript', 24, 316, 600, 168),
        ],
    };
}

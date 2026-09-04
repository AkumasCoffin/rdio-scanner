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

import { Subscription } from "rxjs";

export interface RdioScannerAvoidOptions {
    all?: boolean;
    call?: RdioScannerCall;
    minutes?: number;
    status?: boolean;
    system?: RdioScannerSystem;
    talkgroup?: RdioScannerTalkgroup;
}

export interface RdioScannerBeep {
    begin: number;
    end: number;
    frequency: number;
    type: OscillatorType;
}

export enum RdioScannerBeepStyle {
    Activate = 'activate',
    Deactivate = 'deactivate',
    Denied = 'denied',
}

export interface RdioScannerCall {
    audio?: {
        type: 'Buffer';
        data: number[];
    };
    audioName?: string;
    audioType?: string;
    dateTime: Date;
    frequencies?: RdioScannerCallFrequency[];
    frequency?: number;
    id: number;
    patches: number[];
    source?: number;
    sources?: RdioScannerCallSource[];
    system: number;
    talkgroup: number;
    talkgroupData?: RdioScannerTalkgroup;
    systemData?: RdioScannerSystem;
    transcript?: string;
    hasTranscript?: boolean;
}

export interface RdioScannerCallFrequency {
    errorCount?: number;
    freq?: number;
    len?: number;
    pos?: number;
    spikeCount?: number;
}

export interface RdioScannerCallSource {
    pos?: number;
    src?: number;
}

export interface RdioScannerCategory {
    label: string;
    status: RdioScannerCategoryStatus;
    type: RdioScannerCategoryType;
}

export enum RdioScannerCategoryStatus {
    Off = 'off',
    On = 'on',
    Partial = 'partial',
}

export enum RdioScannerCategoryType {
    Group = 'group',
    Tag = 'tag',
}

export interface RdioScannerConfig {
    afs?: string;
    alerts?: { [name: string]: RdioScannerBeep[] };
    branding?: string;
    dimmerDelay: number | false;
    dualLed?: boolean;
    wigWagLed?: boolean;
    email?: string;
    groups: { [key: string]: { [key: number]: number[] } };
    keypadBeeps: RdioScannerKeypadBeeps | false;
    playbackGoesLive: boolean;
    showListenersCount: boolean;
    sortByGroups: boolean;
    sortByTags: boolean;
    systems: RdioScannerSystem[];
    tags: { [key: string]: { [key: number]: number[] } };
    tagsToggle: boolean;
    time12hFormat: boolean;
    transcriptionEnabled: boolean;
    umamiUrl?: string;
    umamiWebsiteId?: string;
    waitForTranscript?: boolean;
    showRetranscribeButton?: boolean;
    /**
     * Frontend entry points of the plugins enabled on this server, plus any
     * keys those plugins chose to expose. Arrives on the config payload so the
     * webapp can load plugin code without needing an admin session.
     */
    plugins?: RdioScannerPluginEntry[];
    [key: string]: unknown;
}

export interface RdioScannerPluginEntry {
    id: string;
    name: string;
    version: string;
    entry: string;
    base: string;
}

export interface RdioScannerEvent {
    auth?: boolean;
    categories?: RdioScannerCategory[];
    call?: RdioScannerCall;
    config?: RdioScannerConfig;
    deepLinkCall?: number;
    transcriptReady?: { id: number; transcript: string };
    expired?: boolean;
    holdSys?: boolean;
    holdTg?: boolean;
    linked?: boolean;
    listeners?: number;
    livefeedMode?: RdioScannerLivefeedMode;
    map?: RdioScannerLivefeedMap;
    muted?: boolean;
    pause?: boolean;
    playbackList?: RdioScannerPlaybackList;
    playbackPending?: number;
    /**
     * True once a cursor request came back empty: there is nothing past the far
     * end of the loaded list. Replaces the total count the load-more button
     * used to compare against.
     */
    searchExhausted?: boolean;
    queue?: number;
    queueTime?: number;
    queueJumped?: number;
    autoJumpAhead?: boolean;
    autoJumpThreshold?: number;
    streamOpen?: boolean;
    time?: number;
    tooMany?: boolean;
    volume?: number;
    waitForTranscript?: boolean;
    showRetranscribeButton?: boolean;
}

export interface RdioScannerKeypadBeeps {
    [RdioScannerBeepStyle.Activate]: RdioScannerBeep[];
    [RdioScannerBeepStyle.Deactivate]: RdioScannerBeep[];
    [RdioScannerBeepStyle.Denied]: RdioScannerBeep[];
}

export interface RdioScannerLivefeed {
    active: boolean;
    minutes: number | undefined;
    timer: Subscription | undefined;
}

export interface RdioScannerLivefeedMap {
    [key: number]: {
        [key: number]: RdioScannerLivefeed;
    };
}

export enum RdioScannerLivefeedMode {
    Offline = 'offline',
    Online = 'online',
    Playback = 'playback',
}

export interface RdioScannerPlaybackList {
    /**
     * Total matches — only meaningful for offset callers. Cursor callers get 0
     * because the server skips the count(*) for them, so nothing in the webapp
     * may read this; use `results.length`.
     */
    count: number;
    dateStart: Date;
    dateStop: Date;
    options: RdioScannerSearchOptions;
    results: RdioScannerCall[];
}

/**
 * One end of a keyset page. `dateTime` alone is not unique — calls routinely
 * share a timestamp at the stored precision — so the id rides along as the
 * tiebreak, matching the `dateTime, id` ordering the server pages by.
 */
export interface RdioScannerSearchCursor {
    dateTime: string;
    id: number;
}

/**
 * A talkgroup is only unique inside its system, so a multi-talkgroup filter has
 * to travel as pairs. Same shape as `RdioScannerPreset.talkgroups` minus the
 * naming, which is why a saved livefeed preset maps onto it directly.
 */
export interface RdioScannerSearchTalkgroupRef {
    system: number;
    talkgroup: number;
}

export interface RdioScannerSearchOptions {
    /**
     * Legacy single-day anchor with its own ±24h, sort-anchored meaning. Kept
     * because the Android app and plugins still send it; the webapp now uses
     * dateStart/dateStop instead.
     */
    date?: Date;
    /** Inclusive window, RFC3339. Covers one day, a slice of a day, or a span. */
    dateStart?: string;
    dateStop?: string;
    group?: string;
    groups?: string[];
    limit: number;
    /** 'with' or 'without' narrows to calls that have a transcript, or lack one. */
    transcript?: 'with' | 'without';
    offset?: number;
    q?: string;
    sort: number;
    system?: number;
    systems?: number[];
    tag?: string;
    tags?: string[];
    talkgroup?: number;
    talkgroups?: RdioScannerSearchTalkgroupRef[];
    /**
     * Keyset position. Present means "the chunk after this row"; the server
     * ignores `offset` when it is set, so the two are never sent together.
     */
    after?: RdioScannerSearchCursor;
    /**
     * Declares that this caller pages by cursor, which lets the server skip the
     * count(*) even on the first request of a walk — that first page is exactly
     * where the full-table scan used to be paid.
     */
    cursor?: boolean;
}

export interface RdioScannerSystem {
    id: number;
    label: string;
    // A name from LED_COLORS (led-colors.ts). Typed as string rather than a
    // union so a config from a newer or older server never fails the type —
    // unknown names degrade to the default green LED at render time.
    led?: string;
    // Second LED color, shown when the dualLed option is enabled.
    led2?: string;
    order?: number;
    talkgroups: RdioScannerTalkgroup[];
    units: RdioScannerUnit[];
    alert?: string;
}

export interface RdioScannerTalkgroup {
    frequency?: number;
    group: string;
    id: number;
    label: string;
    // See RdioScannerSystem.led / led2.
    led?: string;
    led2?: string;
    name: string;
    tag: string;
    alert?: string;
}

export interface RdioScannerUnit {
    id: number;
    label: string;
}

export interface RdioScannerPreset {
    id: string;
    name: string;
    talkgroups: Array<{
        systemId: number;
        talkgroupId: number;
    }>;
    createdAt: number;
}

export interface RdioScannerPresetExport {
    version: string;
    presets: RdioScannerPreset[];
    exportedAt: number;
}

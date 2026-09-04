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

import { CdkVirtualScrollViewport } from '@angular/cdk/scrolling';
import { HttpClient, HttpHeaders } from '@angular/common/http';
import { AfterViewInit, ChangeDetectorRef, Component, ElementRef, EventEmitter, HostListener, OnDestroy, OnInit, Output, ViewChild } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatSnackBar } from '@angular/material/snack-bar';
import { BehaviorSubject, firstValueFrom } from 'rxjs';
import {
    RdioScannerCall,
    RdioScannerConfig,
    RdioScannerEvent,
    RdioScannerLivefeedMode,
    RdioScannerPlaybackList,
    RdioScannerPreset,
    RdioScannerSearchCursor,
    RdioScannerSearchOptions,
    RdioScannerSearchTalkgroupRef,
} from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';
import { LED_HEX } from '../led-colors';
import { readAdminToken } from '../admin/admin-token';



const FILTERS_STORAGE_KEY = 'rdio-scanner-search-filters';

/**
 * How long a queued transcription may go unanswered before the row stops
 * claiming to be working on it.
 *
 * Generous on purpose: the provider is allowed sixty seconds per attempt and
 * retries a rate-limited one with backoff, so a transcript that is merely slow
 * must not be declared lost while it is still coming.
 */
const TRANSCRIBE_WATCHDOG_MS = 180000;

/**
 * Rows per request. The server caps `limit` at 500; 100 keeps each chunk small
 * enough to render in one frame while still being a meaningful jump.
 */
const CHUNK_SIZE = 100;

/**
 * Fetch the next chunk once the rendered window comes within this many rows of
 * the end. Set to 0 to require the user to press "Load more" every time.
 */
const AUTO_LOAD_WITHIN = 15;

/**
 * Ceiling on the chunks a deep link will walk looking for its call before it
 * gives up and just shows what it has. Without it a share link to a call that
 * has since been filtered out would page the whole database.
 */
const DEEP_LINK_MAX_CHUNKS = 20;

/**
 * How often an open list asks for calls received since it loaded, and how many
 * it asks for.
 *
 * Five seconds is under the length of most transmissions, so a call tends to
 * appear about as it finishes. The count only has to cover a burst between two
 * ticks; anything beyond it is picked up by the tick after, in order, because
 * each request asks for the newest and keeps what it has not already got.
 */
const LIVE_POLL_MS = 5000;
const LIVE_CHUNK_SIZE = 50;

/**
 * How far from the top the list may be scrolled and still take new calls.
 *
 * Rows are added at the front, which pushes everything below them down. That
 * is invisible at the top of the list and intolerable in the middle of it, so
 * a reader who has scrolled away is left alone until they come back.
 */
const LIVE_TOP_PX = 8;

/** One selectable talkgroup, flattened out of the per-system config. */
export interface RdioScannerSearchTalkgroupOption {
    key: string;
    label: string;
    name: string;
    systemId: number;
    systemLabel: string;
    talkgroupId: number;
}

/**
 * One agency and the talkgroups that belong to it. The flat list was unusable
 * on a real config: every agency's Dispatch 1 looked like every other's.
 */
export interface RdioScannerSearchAgencyGroup {
    id: number;
    label: string;
    talkgroups: RdioScannerSearchTalkgroupOption[];
    /** How many of this agency's talkgroups are currently picked. */
    selected: number;
}

/** One active filter, rendered as a removable chip under the filter bar. */
export interface RdioScannerSearchChip {
    kind: 'date' | 'time' | 'system' | 'talkgroup' | 'group' | 'tag' | 'query';
    /** Identifies which value to drop when the chip's × is pressed. */
    value: string;
    label: string;
}

/** How many options a picker renders before it asks the user to narrow down. */
const MAX_RENDERED_OPTIONS = 300;

/**
 * How many options a plain list shows before it offers the rest. The rail is
 * one scrolling column, so a long list is cut short rather than given its own
 * scrollbar — Systems alone would otherwise push Talkgroups off the bottom.
 */
const COLLAPSED_OPTIONS = 8;

/** The spans offered above the two date fields, in days back from today. */
const DATE_PRESETS: { key: string; label: string; days: number }[] = [
    { key: 'today', label: 'Today', days: 0 },
    { key: '7d', label: '7 days', days: 6 },
    { key: '30d', label: '30 days', days: 29 },
];

/**
 * A line in the results list. Calls are the point; the other two are structure
 * the grouping toggle introduces, and exist only when it is on.
 */
export type SearchRow =
    | { kind: 'call'; call: RdioScannerCall | null; showDate: boolean; name: string; patches: string }
    | { kind: 'burst'; count: number; from: Date; to: Date; spanMs: number; services: string[] }
    | { kind: 'quiet'; gapMs: number };

/**
 * Quotes a CSV cell the way a spreadsheet expects.
 *
 * Transcripts are the reason this is not a naive join: radio traffic is full of
 * commas, and a quoted transcript containing a quotation mark has to double it
 * or the row silently splits into two columns halfway through what was said.
 */
function csvCell(value: string): string {
    const text = value ?? '';
    return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** Local-time stamp for the manifest filename, sortable and free of colons. */
function manifestStamp(when: Date): string {
    const pad = (n: number) => `${n}`.padStart(2, '0');
    return `${when.getFullYear()}${pad(when.getMonth() + 1)}${pad(when.getDate())}`
        + `-${pad(when.getHours())}${pad(when.getMinutes())}`;
}

@Component({
    selector: 'rdio-scanner-search',
    styleUrls: ['./search.component.scss'],
    templateUrl: './search.component.html',
})
export class RdioScannerSearchComponent implements AfterViewInit, OnDestroy, OnInit {
    call: RdioScannerCall | undefined;
    callPending: number | undefined;

    /**
     * `range` is a nested group because that is what `mat-date-range-input`
     * binds to. Everything else the picker cannot express — the time-of-day
     * bounds and the four multi-selects — lives in the filters panel.
     */
    form = this.ngFormBuilder.group({
        q: [''],
        sort: [-1],
        range: this.ngFormBuilder.group({
            start: [null as Date | null],
            end: [null as Date | null],
        }),
        timeStart: [''],
        timeStop: [''],
        systems: [[] as number[]],
        talkgroups: [[] as string[]],
        groups: [[] as string[]],
        tags: [[] as string[]],
    });

    private qDebounce: ReturnType<typeof setTimeout> | undefined;

    livefeedOnline = false;
    livefeedPlayback = false;

    playbackList: RdioScannerPlaybackList | undefined;

    /** Panel visibility. One flag drives both the desktop dropdown and the phone sheet. */
    filtersOpen = false;

    /** Free-text narrowing for the talkgroup picker only — never sent to the server. */
    talkgroupQuery = '';

    presets: RdioScannerPreset[] = [];

    optionsSystem: { id: number; label: string }[] = [];
    optionsTalkgroupByAgency: RdioScannerSearchAgencyGroup[] = [];
    optionsGroup: string[] = [];
    optionsTag: string[] = [];

    /** True when a picker had more options than it is willing to draw. */
    talkgroupsTruncated = false;

    /** Agencies the user opened or closed by hand; the rest follow the default. */
    private agencyOpen = new Map<number, boolean>();

    /** Sections the user asked to see in full. */
    private expandedSections = new Set<string>();

    /** Exposed so the template can ask whether a list is long enough to cut. */
    readonly collapsedOptions = COLLAPSED_OPTIONS;

    readonly datePresets = DATE_PRESETS;

    /*
       Selection lookups run once per rendered option per change-detection
       pass. As arrays that is quadratic in the size of the selection, which
       is exactly the case that hurts: take a whole agency and every one of
       its talkgroups then scans every one of its own keys.
    */
    private selectedSystems = new Set<number>();

    private selectedTalkgroups = new Set<string>();

    private tgDebounce: ReturnType<typeof setTimeout> | undefined;

    chips: RdioScannerSearchChip[] = [];

    paused = false;

    results = new BehaviorSubject<RdioScannerCall[]>([]);
    resultsPending = false;

    /** No more chunks behind the cursor — hides "Load more" and stops auto-loading. */
    exhausted = false;

    /** Auto-load is on by default; the button below the list is the manual fallback. */
    autoLoad = AUTO_LOAD_WITHIN > 0;

    time12h = false;

    // Admin-gated retranscribe button. Controlled by the server via CFG.
    showRetranscribeButton = false;

    // Multi-select download state
    selectedCalls = new Set<number>();

    /**
     * Grouping calls into the bursts they arrived in.
     *
     * Off by default: it changes what a row means — a row in a group is part of
     * an incident rather than an isolated call — and that is a choice, not a
     * default anybody should have made for them.
     */
    groupByBurst = false;

    /**
     * Whether an open list keeps up with what is being received.
     *
     * Off by default: a list that rearranges itself while it is being read is
     * the wrong default for a search, which is usually a look at what already
     * happened.
     */
    liveUpdate = false;

    private livePoll: ReturnType<typeof setInterval> | undefined;
    isDownloading = false;

    // Transcript expansion / retranscribe state
    expandedTranscriptId: number | undefined;
    transcribingIds = new Set<number>();

    /** Watchdog timers for queued transcriptions, keyed by call id. */
    private transcribeWatchdogs = new Map<number, number>();

    // Deep-link focus state. When a user lands on ?call=<id>, we highlight
    // that call's row and scroll to it. Cleared on any user-driven form
    // change so normal searches don't carry the highlight.
    highlightedCallId: number | undefined;
    private pendingFocusCallId: number | undefined;
    private highlightClearTimer: ReturnType<typeof setTimeout> | undefined;
    private deepLinkChunks = 0;

    @Output() focusedCall = new EventEmitter<number>();

    private config: RdioScannerConfig | undefined;

    private eventSubscription = this.rdioScannerService.event.subscribe((event: RdioScannerEvent) => this.eventHandler(event));

    @ViewChild(CdkVirtualScrollViewport) private viewport: CdkVirtualScrollViewport | undefined;

    private viewportResize: ResizeObserver | undefined;

    constructor(
        private rdioScannerService: RdioScannerService,
        private ngChangeDetectorRef: ChangeDetectorRef,
        private ngFormBuilder: FormBuilder,
        private ngHttpClient: HttpClient,
        private matSnackBar: MatSnackBar,
        private hostElement: ElementRef,
    ) { }

    // ---------------------------------------------------------------- lifecycle

    ngOnInit(): void {
        this.restoreFilters();

        this.presets = this.rdioScannerService.getPresets();

        // Seed config from the service's tracked state in case the CFG
        // event landed before this component subscribed (the index.html
        // early-WS can finish handshaking before Angular bootstraps the
        // search panel). Only seed if the event hasn't already populated.
        if (!this.config) {
            const cfg = this.rdioScannerService.getConfig();
            if (cfg) {
                this.applyConfig(cfg);
                if (!this.resultsPending && !this.playbackList) {
                    this.searchCalls();
                }
            }
        }
    }

    ngAfterViewInit(): void {
        if (this.liveUpdate) {
            this.startLivePoll();
        }

        const element = this.viewport?.elementRef.nativeElement;

        if (!element || typeof ResizeObserver === 'undefined') {
            return;
        }

        // The panel is a sidenav, so at the moment the viewport initialises it
        // is slid off-screen with no height. The CDK measures the viewport
        // exactly once at that point, records zero, and then renders one
        // buffer's worth of rows for the rest of the session — a short list
        // with empty space under it however tall the panel actually is.
        // Re-measuring on every size change also covers window resize and
        // rotation, which is why this replaces a one-off on panel open.
        this.viewportResize = new ResizeObserver(() => this.viewport?.checkViewportSize());
        this.viewportResize.observe(element);
    }

    ngOnDestroy(): void {
        this.stopLivePoll();
        this.eventSubscription.unsubscribe();
        this.viewportResize?.disconnect();
        if (this.qDebounce) clearTimeout(this.qDebounce);
        if (this.tgDebounce) clearTimeout(this.tgDebounce);
        if (this.highlightClearTimer) clearTimeout(this.highlightClearTimer);
        this.transcribeWatchdogs.forEach((timer) => window.clearTimeout(timer));
        this.transcribeWatchdogs.clear();
    }

    @HostListener('document:keydown.escape')
    onEscape(): void {
        if (this.filtersOpen) {
            this.closeFilters();
        }
    }

    // ------------------------------------------------------------ filter panel

    toggleFilters(): void {
        this.filtersOpen = !this.filtersOpen;

        if (this.filtersOpen) {
            this.refreshOptions();
        }
    }

    closeFilters(): void {
        this.filtersOpen = false;

        this.ngChangeDetectorRef.detectChanges();
    }

    /**
     * Number shown on the Filters button. The text query and the sort live in
     * the bar where they are already visible, so neither counts.
     */
    activeFilterCount(): number {
        return this.chips.filter((chip) => chip.kind !== 'query').length;
    }

    // ---------------------------------------------------------- filter options

    /**
     * Rebuilds the four option lists. Each list is narrowed by the *other*
     * selections, so picking a system shortens the talkgroup list rather than
     * leaving the user to scroll past 49 systems' worth of entries. A selection
     * never narrows its own list — that would make the chosen values vanish
     * from the picker they were chosen in.
     */
    refreshOptions(): void {
        const config = this.config;

        if (!config) {
            return;
        }

        const systems = this.form.value.systems as number[];
        const groups = this.form.value.groups as string[];
        const tags = this.form.value.tags as string[];

        this.selectedSystems = new Set(systems);
        this.selectedTalkgroups = new Set(this.form.value.talkgroups as string[]);

        const matchesScope = (group: string, tag: string) =>
            (!groups.length || groups.includes(group)) && (!tags.length || tags.includes(tag));

        this.optionsSystem = config.systems
            .filter((system) => system.talkgroups.some((talkgroup) => matchesScope(talkgroup.group, talkgroup.tag)))
            .map((system) => ({ id: system.id, label: system.label }));

        const query = this.talkgroupQuery.trim().toLowerCase();

        const byAgency: RdioScannerSearchAgencyGroup[] = [];
        let truncated = false;

        for (const system of config.systems) {
            if (systems.length && !systems.includes(system.id)) {
                continue;
            }

            const agency: RdioScannerSearchAgencyGroup = {
                id: system.id,
                label: system.label,
                talkgroups: [],
                selected: 0,
            };

            for (const talkgroup of system.talkgroups) {
                if (!matchesScope(talkgroup.group, talkgroup.tag)) {
                    continue;
                }

                if (query && !`${talkgroup.label} ${talkgroup.name} ${system.label}`.toLowerCase().includes(query)) {
                    continue;
                }

                // The cap is per agency, not across the whole list: a closed
                // agency renders no options at all, so one enormous system is
                // no reason for the fiftieth system to lose its header.
                if (agency.talkgroups.length >= MAX_RENDERED_OPTIONS) {
                    truncated = true;
                    break;
                }

                // Same rule the result rows use: a name that only spells out
                // the agency and the label again is noise in a 268 px column.
                const name = `${talkgroup.name ?? ''}`.trim();
                const redundant = !name
                    || name === talkgroup.label
                    || name.toLowerCase() === `${system.label} ${talkgroup.label}`.toLowerCase();

                const option: RdioScannerSearchTalkgroupOption = {
                    key: `${system.id}:${talkgroup.id}`,
                    label: talkgroup.label,
                    name: redundant ? '' : name,
                    systemId: system.id,
                    systemLabel: system.label,
                    talkgroupId: talkgroup.id,
                };

                agency.talkgroups.push(option);

                if (this.selectedTalkgroups.has(option.key)) {
                    agency.selected++;
                }
            }

            if (agency.talkgroups.length) {
                byAgency.push(agency);
            }
        }

        this.optionsTalkgroupByAgency = byAgency;
        this.talkgroupsTruncated = truncated;

        const inScope = (predicate: (group: string, tag: string) => boolean) => config.systems
            .filter((system) => !systems.length || systems.includes(system.id))
            .flatMap((system) => system.talkgroups)
            .some((talkgroup) => predicate(talkgroup.group, talkgroup.tag));

        this.optionsGroup = Object.keys(config.groups)
            .filter((group) => inScope((g, tag) => g === group && (!tags.length || tags.includes(tag))))
            .sort((a, b) => a.localeCompare(b));

        this.optionsTag = Object.keys(config.tags)
            .filter((tag) => inScope((group, t) => t === tag && (!groups.length || groups.includes(group))))
            .sort((a, b) => a.localeCompare(b));
    }

    onTalkgroupQuery(value: string): void {
        this.talkgroupQuery = value;

        // Rebuilding the agency structure means walking every talkgroup in the
        // config, so it waits for a pause rather than running per keystroke —
        // the same treatment the transcript query already gets.
        if (this.tgDebounce) {
            clearTimeout(this.tgDebounce);
        }

        this.tgDebounce = setTimeout(() => {
            this.tgDebounce = undefined;

            this.refreshOptions();

            this.ngChangeDetectorRef.detectChanges();
        }, 180);
    }

    // --------------------------------------------------------------- selection

    isSystemSelected(id: number): boolean {
        return this.selectedSystems.has(id);
    }

    toggleSystem(id: number): void {
        const systems = (this.form.value.systems as number[]).slice();
        const index = systems.indexOf(id);

        if (index >= 0) {
            systems.splice(index, 1);
        } else {
            systems.push(id);
        }

        // A talkgroup filter outside the chosen systems can only ever match
        // nothing, since the server ANDs the two. Dropping those pairs turns a
        // guaranteed-empty search into the one the user plainly meant.
        const talkgroups = systems.length
            ? (this.form.value.talkgroups as string[]).filter((key) => systems.includes(+key.split(':')[0]))
            : (this.form.value.talkgroups as string[]);

        this.form.patchValue({ systems, talkgroups });

        this.applyFilters();
    }

    isTalkgroupSelected(key: string): boolean {
        return this.selectedTalkgroups.has(key);
    }

    toggleTalkgroup(key: string): void {
        const talkgroups = (this.form.value.talkgroups as string[]).slice();
        const index = talkgroups.indexOf(key);

        if (index >= 0) {
            talkgroups.splice(index, 1);
        } else {
            talkgroups.push(key);
        }

        this.form.patchValue({ talkgroups });

        this.applyFilters();
    }

    /**
     * Agencies stay closed by default so a config with hundreds of talkgroups
     * still fits the rail. One being searched, one holding a selection, and a
     * lone agency open themselves; closing it by hand then sticks.
     */
    isAgencyOpen(agency: RdioScannerSearchAgencyGroup): boolean {
        const chosen = this.agencyOpen.get(agency.id);

        if (chosen !== undefined) {
            return chosen;
        }

        return !!this.talkgroupQuery.trim()
            || agency.selected > 0
            || this.optionsTalkgroupByAgency.length === 1;
    }

    /*
       Without these, every filter change replaces the agency objects and Angular
       tears down and rebuilds all three hundred option rows — which also threw
       away which agencies were open.
    */
    isSectionExpanded(key: string): boolean {
        return this.expandedSections.has(key);
    }

    toggleSection(key: string): void {
        if (!this.expandedSections.delete(key)) {
            this.expandedSections.add(key);
        }
    }

    /** The first few options of a plain list, or all of them once asked. */
    visibleOptions<T>(options: T[], key: string): T[] {
        return this.isSectionExpanded(key) ? options : options.slice(0, COLLAPSED_OPTIONS);
    }

    /**
     * Agencies follow the same rule, except that typing in the talkgroup filter
     * means every agency still holding a match has to be reachable.
     */
    get visibleAgencies(): RdioScannerSearchAgencyGroup[] {
        return this.talkgroupQuery.trim()
            ? this.optionsTalkgroupByAgency
            : this.visibleOptions(this.optionsTalkgroupByAgency, 'agencies');
    }

    trackAgency(_index: number, agency: RdioScannerSearchAgencyGroup): number {
        return agency.id;
    }

    trackTalkgroupOption(_index: number, option: RdioScannerSearchTalkgroupOption): string {
        return option.key;
    }

    toggleAgency(agency: RdioScannerSearchAgencyGroup): void {
        this.agencyOpen.set(agency.id, !this.isAgencyOpen(agency));
    }

    /** Takes the whole agency in or out of the talkgroup selection. */
    toggleAgencySelection(agency: RdioScannerSearchAgencyGroup, event: Event): void {
        event.stopPropagation();

        const talkgroups = new Set(this.form.value.talkgroups as string[]);
        const keys = agency.talkgroups.map((talkgroup) => talkgroup.key);

        if (agency.selected >= keys.length) {
            keys.forEach((key) => talkgroups.delete(key));
        } else {
            keys.forEach((key) => talkgroups.add(key));
        }

        this.form.patchValue({ talkgroups: [...talkgroups] });

        this.applyFilters();
    }

    /** `yyyy-MM-dd`, the only format a native date input accepts. */
    dateInput(which: 'start' | 'end'): string {
        const value = (this.form.value.range as { start: Date | null; end: Date | null } | undefined)?.[which];

        if (!(value instanceof Date) || isNaN(value.getTime())) {
            return '';
        }

        const pad = (n: number) => `${n}`.padStart(2, '0');

        return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`;
    }

    setDate(which: 'start' | 'end', event: Event): void {
        const raw = (event.target as HTMLInputElement).value;

        // A native date input reports an empty value until every segment is
        // filled, so a half-typed date looks exactly like a cleared one. Acting
        // on it would null the model, and the binding would then wipe the
        // segments already typed. Emptiness is settled on blur instead.
        if (!raw) {
            return;
        }

        const parsed = this.parseDateInput(raw);
        const current = this.form.value.range as { start: Date | null; end: Date | null };
        const range = { start: current?.start ?? null, end: current?.end ?? null, [which]: parsed };

        // One end past the other describes nothing, so the other end follows.
        if (range.start && range.end && range.start > range.end) {
            if (which === 'start') {
                range.end = parsed;
            } else {
                range.start = parsed;
            }
        }

        this.form.patchValue({ range });

        this.applyFilters();
    }

    /**
     * Settles the field on blur: a date left incomplete is no date, and one
     * left valid is snapped back to whatever is actually filtering.
     */
    commitDate(which: 'start' | 'end', event: Event): void {
        const input = event.target as HTMLInputElement;

        if (input.value) {
            input.value = this.dateInput(which);

            return;
        }

        const current = this.form.value.range as { start: Date | null; end: Date | null };

        if (!current?.[which]) {
            return;
        }

        this.form.patchValue({ range: { start: current.start, end: current.end, [which]: null } });

        this.applyFilters();
    }

    applyDatePreset(preset: { days: number }): void {
        const end = new Date();
        end.setHours(0, 0, 0, 0);

        const start = new Date(end);
        start.setDate(start.getDate() - preset.days);

        this.form.patchValue({ range: { start, end } });

        this.applyFilters();
    }

    isDatePreset(preset: { days: number }): boolean {
        const end = new Date();
        end.setHours(0, 0, 0, 0);

        const start = new Date(end);
        start.setDate(start.getDate() - preset.days);

        return this.dateInput('start') === this.asDateInput(start) && this.dateInput('end') === this.asDateInput(end);
    }

    clearDates(): void {
        this.form.patchValue({ range: { start: null, end: null } });

        this.applyFilters();
    }

    hasDates(): boolean {
        return !!this.dateInput('start') || !!this.dateInput('end');
    }

    private asDateInput(value: Date): string {
        const pad = (n: number) => `${n}`.padStart(2, '0');

        return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`;
    }

    /**
     * Parsed as a local date. `new Date('2026-08-19')` is UTC midnight, which
     * is the day before for anyone west of Greenwich — and the whole filter is
     * expressed in the operator's own day boundaries.
     */
    private parseDateInput(raw: string): Date | null {
        const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw.trim());

        return match ? new Date(+match[1], +match[2] - 1, +match[3]) : null;
    }

    isGroupSelected(group: string): boolean {
        return (this.form.value.groups as string[]).includes(group);
    }

    toggleGroup(group: string): void {
        this.form.patchValue({ groups: this.toggled(this.form.value.groups as string[], group) });

        this.applyFilters();
    }

    isTagSelected(tag: string): boolean {
        return (this.form.value.tags as string[]).includes(tag);
    }

    toggleTag(tag: string): void {
        this.form.patchValue({ tags: this.toggled(this.form.value.tags as string[], tag) });

        this.applyFilters();
    }

    private toggled(values: string[], value: string): string[] {
        const next = values.slice();
        const index = next.indexOf(value);

        if (index >= 0) {
            next.splice(index, 1);
        } else {
            next.push(value);
        }

        return next;
    }

    // ----------------------------------------------------------------- presets

    /**
     * A saved livefeed preset is already a list of `{systemId, talkgroupId}`,
     * which is the talkgroup filter in everything but field names. Applying one
     * replaces the talkgroup selection rather than adding to it — a preset is a
     * whole answer to "what am I listening to", not an ingredient.
     */
    applyPreset(preset: RdioScannerPreset): void {
        const talkgroups = (preset.talkgroups || []).map(({ systemId, talkgroupId }) => `${systemId}:${talkgroupId}`);

        this.form.patchValue({ talkgroups, systems: [] });

        this.applyFilters();
    }

    isPresetApplied(preset: RdioScannerPreset): boolean {
        const selected = this.form.value.talkgroups as string[];
        const keys = (preset.talkgroups || []).map(({ systemId, talkgroupId }) => `${systemId}:${talkgroupId}`);

        return keys.length > 0
            && keys.length === selected.length
            && keys.every((key) => selected.includes(key));
    }

    // ------------------------------------------------------------------- chips

    private rebuildChips(): void {
        const chips: RdioScannerSearchChip[] = [];
        const value = this.form.value;

        const start = value.range?.start as Date | null;
        const end = value.range?.end as Date | null;

        if (start || end) {
            chips.push({ kind: 'date', value: 'range', label: this.describeRange(start, end) });
        }

        if (value.timeStart || value.timeStop) {
            chips.push({
                kind: 'time',
                value: 'time',
                label: `${value.timeStart || '00:00'} – ${value.timeStop || '23:59'}`,
            });
        }

        for (const id of value.systems as number[]) {
            chips.push({ kind: 'system', value: `${id}`, label: this.systemLabel(id) });
        }

        for (const key of value.talkgroups as string[]) {
            chips.push({ kind: 'talkgroup', value: key, label: this.talkgroupLabel(key) });
        }

        for (const group of value.groups as string[]) {
            chips.push({ kind: 'group', value: group, label: group });
        }

        for (const tag of value.tags as string[]) {
            chips.push({ kind: 'tag', value: tag, label: tag });
        }

        const q = (value.q || '').trim();

        if (q) {
            chips.push({ kind: 'query', value: q, label: `“${q}”` });
        }

        this.chips = chips;
    }

    removeChip(chip: RdioScannerSearchChip): void {
        switch (chip.kind) {
            case 'date':
                this.form.patchValue({ range: { start: null, end: null }, timeStart: '', timeStop: '' });
                break;

            case 'time':
                this.form.patchValue({ timeStart: '', timeStop: '' });
                break;

            case 'system':
                this.form.patchValue({ systems: (this.form.value.systems as number[]).filter((id) => `${id}` !== chip.value) });
                break;

            case 'talkgroup':
                this.form.patchValue({ talkgroups: (this.form.value.talkgroups as string[]).filter((key) => key !== chip.value) });
                break;

            case 'group':
                this.form.patchValue({ groups: (this.form.value.groups as string[]).filter((group) => group !== chip.value) });
                break;

            case 'tag':
                this.form.patchValue({ tags: (this.form.value.tags as string[]).filter((tag) => tag !== chip.value) });
                break;

            case 'query':
                this.form.patchValue({ q: '' });
                break;
        }

        this.applyFilters();
    }

    private systemLabel(id: number): string {
        return this.config?.systems.find((system) => system.id === id)?.label ?? `System ${id}`;
    }

    private talkgroupLabel(key: string): string {
        const [systemId, talkgroupId] = key.split(':').map((part) => +part);
        const system = this.config?.systems.find((s) => s.id === systemId);
        const talkgroup = system?.talkgroups.find((tg) => tg.id === talkgroupId);

        return talkgroup ? `${talkgroup.label}` : `${systemId}:${talkgroupId}`;
    }

    private describeRange(start: Date | null, end: Date | null): string {
        const fmt = (d: Date) => d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });

        if (start && end && start.toDateString() !== end.toDateString()) {
            return `${fmt(start)} – ${fmt(end)}`;
        }

        return fmt((start || end) as Date);
    }

    // ---------------------------------------------------------------- searching

    /**
     * Everything that changes what the server should return funnels through
     * here: stop playback, drop the deep-link highlight, redraw the chips,
     * remember the filters, then start a fresh (non-cursor) search.
     */
    applyFilters(): void {
        if (this.livefeedPlayback) {
            this.rdioScannerService.stopPlaybackMode();
        }

        this.highlightedCallId = undefined;
        this.pendingFocusCallId = undefined;
        this.deepLinkChunks = 0;

        this.refreshOptions();
        this.rebuildChips();
        this.persistFilters();

        this.searchCalls();
    }

    /** Kept for the template's `(change)` bindings and the parent component. */
    formChangeHandler(): void {
        this.applyFilters();
    }

    onQueryInput(): void {
        if (this.qDebounce) {
            clearTimeout(this.qDebounce);
        }
        this.qDebounce = setTimeout(() => {
            this.applyFilters();
        }, 600);
    }

    resetForm(): void {
        this.form.reset({
            q: '',
            sort: -1,
            range: { start: null, end: null },
            timeStart: '',
            timeStop: '',
            systems: [],
            talkgroups: [],
            groups: [],
            tags: [],
        });

        this.talkgroupQuery = '';

        this.applyFilters();
    }

    /**
     * Fresh search — replaces whatever is loaded. `_force` is vestigial but the
     * parent template still calls this with no arguments when the panel opens.
     */
    searchCalls(_force = false): void {
        this.resultsPending = true;
        this.exhausted = false;

        this.viewport?.scrollToOffset(0);

        // Intentionally NOT disabling the form here — locking the transcript
        // input out while a search is in flight blocks the user from typing
        // and causes debounced queries to swallow keystrokes. The progress
        // bar is already visible to signal pending state, and stale LCL
        // responses don't cause issues because the server keeps the most
        // recent result (newer responses overwrite older).
        this.rdioScannerService.searchCalls(this.buildOptions());
    }

    /** Next chunk, appended to what is on screen. */
    loadMore(): void {
        if (this.resultsPending || this.exhausted) {
            return;
        }

        const cursor = this.rdioScannerService.searchCursor();

        if (!cursor) {
            this.searchCalls();

            return;
        }

        this.resultsPending = true;

        this.rdioScannerService.searchCalls(this.buildOptions(cursor));
    }

    /**
     * The list grows without bound, so the only "am I near the end" question
     * that means anything is about the rendered window, not the scroll offset.
     */
    onScrolledIndexChange(): void {
        if (!this.autoLoad || this.resultsPending || this.exhausted || !this.viewport) {
            return;
        }

        if (this.viewport.getRenderedRange().end >= this.results.value.length - AUTO_LOAD_WITHIN) {
            this.loadMore();
        }
    }

    private buildOptions(after?: RdioScannerSearchCursor): RdioScannerSearchOptions {
        const value = this.form.value;

        const options: RdioScannerSearchOptions = {
            limit: CHUNK_SIZE,
            sort: value.sort,
            // Tells the server this caller pages by cursor, so it skips the
            // count(*) on the first request too — that first page is exactly
            // where the scan the numbered pager needed used to be paid.
            cursor: true,
        };

        const window = this.dateWindow();

        if (window.start) {
            options.dateStart = window.start;
        }

        if (window.stop) {
            options.dateStop = window.stop;
        }

        const q = typeof value.q === 'string' ? value.q.trim() : '';

        if (q) {
            options.q = q;
        }

        const systems = value.systems as number[];

        if (systems.length) {
            options.systems = systems.slice();
        }

        const talkgroups = value.talkgroups as string[];

        if (talkgroups.length) {
            options.talkgroups = talkgroups.map((key): RdioScannerSearchTalkgroupRef => {
                const [system, talkgroup] = key.split(':').map((part) => +part);

                return { system, talkgroup };
            });
        }

        const groups = value.groups as string[];

        if (groups.length) {
            options.groups = groups.slice();
        }

        const tags = value.tags as string[];

        if (tags.length) {
            options.tags = tags.slice();
        }

        // Never both: the cursor *is* the position, and an offset on top of it
        // would skip a chunk's worth of rows on every request after the first.
        if (after) {
            options.after = after;
        }

        return options;
    }

    /**
     * Folds the date range and the time-of-day bounds into one inclusive
     * window. A single day is the range picker with both ends on that day; a
     * slice of a day is that plus the time bounds; a span of days is both ends
     * on different days, with the times bounding the span's first and last
     * moment rather than repeating per day.
     */
    private dateWindow(): { start?: string; stop?: string } {
        const start = this.form.value.range?.start as Date | null;
        const end = this.form.value.range?.end as Date | null;

        if (!start && !end) {
            return {};
        }

        const from = (start || end) as Date;
        const to = (end || start) as Date;

        const opens = this.parseTime(this.form.value.timeStart) || [0, 0];
        const closes = this.parseTime(this.form.value.timeStop) || [23, 59];

        const dateStart = new Date(from.getFullYear(), from.getMonth(), from.getDate(), opens[0], opens[1], 0, 0);
        // Inclusive to the end of the chosen minute: the input has no seconds,
        // so "to 17:00" plainly means through 17:00, not up to it.
        const dateStop = new Date(to.getFullYear(), to.getMonth(), to.getDate(), closes[0], closes[1], 59, 999);

        return { start: dateStart.toISOString(), stop: dateStop.toISOString() };
    }

    private parseTime(value: unknown): [number, number] | undefined {
        if (typeof value !== 'string') {
            return undefined;
        }

        const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim());

        if (!match) {
            return undefined;
        }

        const hours = Math.min(23, +match[1]);
        const minutes = Math.min(59, +match[2]);

        return [hours, minutes];
    }

    // ------------------------------------------------------------- persistence

    private persistFilters(): void {
        try {
            const value = this.form.value;
            const start = value.range?.start as Date | null;
            const end = value.range?.end as Date | null;

            window?.localStorage?.setItem(FILTERS_STORAGE_KEY, JSON.stringify({
                q: value.q,
                sort: value.sort,
                start: start ? start.toISOString() : null,
                end: end ? end.toISOString() : null,
                timeStart: value.timeStart,
                timeStop: value.timeStop,
                systems: value.systems,
                talkgroups: value.talkgroups,
                groups: value.groups,
                tags: value.tags,
                groupByBurst: this.groupByBurst,
                liveUpdate: this.liveUpdate,
            }));
        } catch (_) {
            // Private-mode / quota. Losing the last filters is not worth a throw.
        }
    }

    private restoreFilters(): void {
        try {
            const stored = window?.localStorage?.getItem(FILTERS_STORAGE_KEY);

            if (!stored) {
                return;
            }

            const saved = JSON.parse(stored);
            const date = (value: unknown) => {
                const parsed = typeof value === 'string' ? new Date(value) : null;

                return parsed && !isNaN(parsed.getTime()) ? parsed : null;
            };

            this.form.patchValue({
                q: typeof saved.q === 'string' ? saved.q : '',
                sort: saved.sort === 1 ? 1 : -1,
                range: { start: date(saved.start), end: date(saved.end) },
                timeStart: typeof saved.timeStart === 'string' ? saved.timeStart : '',
                timeStop: typeof saved.timeStop === 'string' ? saved.timeStop : '',
                systems: Array.isArray(saved.systems) ? saved.systems.filter((id: unknown) => typeof id === 'number') : [],
                talkgroups: Array.isArray(saved.talkgroups) ? saved.talkgroups.filter((key: unknown) => typeof key === 'string') : [],
                groups: Array.isArray(saved.groups) ? saved.groups.filter((g: unknown) => typeof g === 'string') : [],
                tags: Array.isArray(saved.tags) ? saved.tags.filter((t: unknown) => typeof t === 'string') : [],
            });

            this.groupByBurst = saved.groupByBurst === true;
            this.liveUpdate = saved.liveUpdate === true;

            this.rebuildChips();
        } catch (_) {
            // Corrupt entry: fall back to the unfiltered default rather than
            // leaving the panel unusable.
        }
    }

    // ------------------------------------------------------------ deep linking

    isHighlighted(id: number | undefined): boolean {
        return !!id && this.highlightedCallId === id;
    }

    trackCall = (_i: number, row: RdioScannerCall): number | string => {
        return row?.id ?? _i;
    };

    focusCall(id: number): void {
        if (!id) return;

        this.highlightedCallId = id;
        this.pendingFocusCallId = id;
        this.deepLinkChunks = 0;

        if (this.highlightClearTimer) {
            clearTimeout(this.highlightClearTimer);
            this.highlightClearTimer = undefined;
        }

        // Kick playback and metadata fetch. The `call` event handler below
        // uses the returned dateTime to set the date filter, re-search, and
        // highlight. Playback runs alongside, which is fine — the user asked
        // for a clickable share link that takes them to the call.
        this.rdioScannerService.loadAndPlay(id);

        // Also fire an initial search immediately so the panel isn't blank
        // while the call metadata is in flight.
        if (!this.resultsPending) {
            this.searchCalls();
        }

        this.ngChangeDetectorRef.detectChanges();
    }

    private clearHighlightSoon(delayMs = 8000): void {
        if (this.highlightClearTimer) {
            clearTimeout(this.highlightClearTimer);
        }
        this.highlightClearTimer = setTimeout(() => {
            this.highlightedCallId = undefined;
            this.highlightClearTimer = undefined;
            this.ngChangeDetectorRef.detectChanges();
        }, delayMs);
    }

    /**
     * Brings the highlighted row into view. With virtual scrolling the row may
     * not be in the DOM at all, so the viewport is told the index first and the
     * element is only looked up once that render has happened.
     */
    private scrollHighlightedIntoView(index: number): void {
        this.viewport?.scrollToIndex(index, 'smooth');

        const host = this.hostElement?.nativeElement as HTMLElement | undefined;

        if (!host) return;

        setTimeout(() => {
            const row = host.querySelector(`[data-call-row="${this.highlightedCallId}"]`) as HTMLElement | null;
            if (row?.scrollIntoView) {
                row.scrollIntoView({ behavior: 'smooth', block: 'center' });
            }
            this.focusedCall.emit(this.highlightedCallId);
        }, 120);
    }

    // ------------------------------------------------------------ row features

    isAdminAuthenticated(): boolean {
        return !!readAdminToken();
    }

    toggleTranscript(id: number | undefined): void {
        if (!id) return;
        if (this.expandedTranscriptId === id) {
            this.expandedTranscriptId = undefined;
            return;
        }
        this.expandedTranscriptId = id;

        const call = this.findCall(id);
        if (call && call.hasTranscript && call.transcript === undefined) {
            this.loadTranscript(id);
        }
    }

    private findCall(id: number): RdioScannerCall | undefined {
        return this.results.value.find((c) => c?.id === id);
    }

    private async loadTranscript(id: number): Promise<void> {
        const text = await this.rdioScannerService.fetchTranscript(id);
        this.applyTranscript(id, text);
    }

    private applyTranscript(id: number, transcript: string): void {
        const current = this.results.value.slice();
        const idx = current.findIndex((c) => c?.id === id);
        if (idx >= 0 && current[idx]) {
            current[idx] = { ...current[idx], transcript, hasTranscript: !!transcript };
            this.results.next(current);
        }
        if (this.playbackList) {
            const plIdx = this.playbackList.results.findIndex((c) => c.id === id);
            if (plIdx >= 0) {
                this.playbackList.results[plIdx] = {
                    ...this.playbackList.results[plIdx],
                    transcript,
                    hasTranscript: !!transcript,
                };
            }
        }
        this.ngChangeDetectorRef.detectChanges();
    }

    isTranscriptExpanded(id: number | undefined): boolean {
        return !!id && this.expandedTranscriptId === id;
    }

    isTranscribing(id: number | undefined): boolean {
        return !!id && this.transcribingIds.has(id);
    }

    async transcribeCall(id: number | undefined): Promise<void> {
        if (!id || this.transcribingIds.has(id)) return;

        const token = readAdminToken();
        if (!token) {
            this.matSnackBar.open('Sign in as admin to request a transcription.', '', { duration: 4000 });
            return;
        }

        this.transcribingIds.add(id);
        this.expandedTranscriptId = id;
        this.ngChangeDetectorRef.detectChanges();

        try {
            const url = `${window.location.href}/../api/admin/transcribe`;
            const res = await firstValueFrom(this.ngHttpClient.post<{ id: number; transcript?: string }>(
                url,
                { id, manual: false },
                { headers: new HttpHeaders({ Authorization: token }), responseType: 'json' },
            ));

            // A transcript in the reply means the plugin did the work inline
            // and this is done. Anything else means it took the request as a
            // request — the job is queued, and the result will arrive as a
            // push. Both shapes are accepted because the plugin that answers
            // this route is versioned separately from the server.
            if (typeof res?.transcript === 'string') {
                this.transcribingIds.delete(id);
                this.applyTranscript(id, res.transcript);
                return;
            }

            this.startTranscribeWatchdog(id);
        } catch (err: any) {
            this.transcribingIds.delete(id);

            const msg = err?.error?.error || err?.message || 'Transcription failed.';
            this.matSnackBar.open(msg, '', { duration: 5000 });
        } finally {
            this.ngChangeDetectorRef.detectChanges();
        }
    }

    /**
     * Stops a queued transcription from spinning forever.
     *
     * The push is the only thing that reports success, and it can legitimately
     * be a minute or two away behind a busy provider — but it can also never
     * come, if the audio was too short to transcribe or the model returned only
     * silence. Both are outcomes the server logs and neither sends a push, so
     * without this the row keeps a spinner until the page is reloaded.
     */
    private startTranscribeWatchdog(id: number): void {
        this.clearTranscribeWatchdog(id);

        this.transcribeWatchdogs.set(id, window.setTimeout(() => {
            this.transcribeWatchdogs.delete(id);

            if (!this.transcribingIds.delete(id)) return;

            this.matSnackBar.open('Still transcribing, or nothing usable in the audio.', '', { duration: 5000 });
            this.ngChangeDetectorRef.detectChanges();
        }, TRANSCRIBE_WATCHDOG_MS));
    }

    private clearTranscribeWatchdog(id: number): void {
        const timer = this.transcribeWatchdogs.get(id);

        if (timer !== undefined) {
            window.clearTimeout(timer);
            this.transcribeWatchdogs.delete(id);
        }
    }

    download(id: number): void {
        this.rdioScannerService.loadAndDownload(id);
    }

    play(id: number): void {
        this.rdioScannerService.loadAndPlay(id);
    }

    stop(): void {
        if (this.livefeedPlayback) {
            this.rdioScannerService.stopPlaybackMode();

        } else {
            this.rdioScannerService.stop();
        }
    }

    // Multi-select methods
    /**
     * How long a silence has to be before it separates one burst from the next.
     *
     * Two minutes because that is roughly how radio traffic actually behaves:
     * units working an incident talk over each other in bursts seconds apart,
     * and the gap to the next unrelated call is minutes. Too short and one
     * incident splits into a dozen groups; too long and a quiet night becomes
     * one group holding everything.
     */
    /**
     * A burst is one talkgroup transmitting back and forth: calls on the same
     * talkgroup no more than this far apart belong to the same exchange. The
     * gap is between call starts, so it swallows the transmission itself plus
     * the pause before the reply — on real traffic consecutive turns of one
     * conversation commonly start 15–25s apart.
     */
    private static readonly BURST_GAP_MS = 30 * 1000;

    /**
     * How far, in list positions, grouping may move a call from where it
     * naturally sits. A run only absorbs calls born within this many rows of
     * where it started, which bounds both the pull-up of a joining call and
     * the push-down of the calls it leaps over. A conversation longer than
     * the window splits into consecutive runs — invisible, since they render
     * back to back.
     */
    private static readonly BURST_WINDOW = 8;

    /**
     * The loaded calls, with a burst header in front of each group.
     *
     * Derived rather than stored: the list grows as you scroll, and a call
     * arriving at the end can only ever extend the last group or start a new
     * one, so recomputing is both simpler and correct where incremental
     * bookkeeping would drift.
     */
    get rows(): SearchRow[] {
        const calls = this.results.value;
        const cached = this.rowCache;

        // The getter is read on every change-detection pass, and rebuilding it
        // allocates a row per loaded call. The results array is replaced rather
        // than mutated whenever it changes, so its identity is a sound key.
        if (cached && cached.calls === calls && cached.grouped === this.groupByBurst) {
            return cached.rows;
        }

        const rows = this.buildRows(calls);

        this.rowCache = { calls, grouped: this.groupByBurst, rows };

        return rows;
    }

    private rowCache: { calls: RdioScannerCall[]; grouped: boolean; rows: SearchRow[] } | undefined;

    /**
     * Groups an exchange on one talkgroup into a contiguous run, even when
     * traffic on other talkgroups landed in between. A call joins its
     * talkgroup's open burst while the gap to that burst's newest member is
     * within BURST_GAP_MS; anything else starts a run of its own, in list
     * position.
     */
    private buildRows(calls: RdioScannerCall[]): SearchRow[] {
        if (!this.groupByBurst) {
            return calls.map((call, index) => this.callRow(call, calls[index - 1]));
        }

        interface Burst {
            calls: RdioScannerCall[];
            last: number;
            firstIndex: number;
        }

        // The list is time-ordered (either direction), so once a talkgroup's
        // newest member is further than the gap from the point of iteration,
        // no later call can rejoin it — replacing the map entry is closure.
        const bursts: Burst[] = [];
        const open = new Map<string, Burst>();

        calls.forEach((call, index) => {
            if (!call) {
                return;
            }

            const key = `${call.system}:${call.talkgroup}`;
            const at = new Date(call.dateTime).getTime();
            const current = open.get(key);

            if (current
                && Math.abs(at - current.last) <= RdioScannerSearchComponent.BURST_GAP_MS
                && index - current.firstIndex <= RdioScannerSearchComponent.BURST_WINDOW) {
                current.calls.push(call);
                current.last = at;

                return;
            }

            const burst: Burst = { calls: [call], last: at, firstIndex: index };

            bursts.push(burst);
            open.set(key, burst);
        });

        // No headers, no markers: grouping only reorders. The rows read
        // exactly as they do ungrouped; an exchange's calls simply sit
        // together, at the list position of the exchange's first call, so the
        // list stays time-ordered between exchanges and time-ordered inside
        // each one.
        const out: SearchRow[] = [];
        let emitted: RdioScannerCall | null = null;

        for (const burst of bursts) {
            // Compared against the last call emitted anywhere, not the last in
            // this group: otherwise every group reprints the date, and on an
            // evening of traffic that is the same date over and over.
            for (const call of burst.calls) {
                out.push(this.callRow(call, emitted));
                emitted = call;
            }
        }

        return out;
    }

    /**
     * The colour this call is drawn with: its talkgroup LED, falling back to the
     * system LED.
     *
     * This is the one piece of identity the search view never used. A page of
     * results is mostly one service repeating, and colouring the rail by the
     * colour the operator already assigned makes that legible before any of the
     * text is read.
     */
    ledFor(call: RdioScannerCall): string {
        const name = call?.talkgroupData?.led || call?.systemData?.led;

        return name ? (LED_HEX[name] ?? LED_HEX['blue']) : 'var(--rdio-search-rail)';
    }

    /** Compact human span: 45s, 4m 12s, 1h 20m. */
    formatSpan(ms: number): string {
        const total = Math.max(0, Math.round(ms / 1000));

        if (total < 60) {
            return `${total}s`;
        }

        const minutes = Math.floor(total / 60);
        const seconds = total % 60;

        if (minutes < 60) {
            return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
        }

        const hours = Math.floor(minutes / 60);

        return `${hours}h ${minutes % 60}m`;
    }

    /**
     * The row itself plays, which is what lets the controls stay hidden until
     * hover — but only when the click was not aimed at something else in it.
     */
    rowClicked(row: RdioScannerCall, event: Event): void {
        const target = event.target as HTMLElement | null;

        if (target?.closest('button, a, input, .row-tools, .row-pick')) {
            return;
        }

        if (row.id === this.call?.id) {
            this.stop();
        } else {
            this.play(+row.id);
        }
    }

    /**
     * One call, with the two things the row cannot work out for itself.
     *
     * The date is dropped when it matches the row above: a page of one evening's
     * traffic repeated "Wed 19 Aug" on every line, which is noise the moment it
     * stops changing. And the talkgroup name is dropped when it only restates
     * the system and label already beside it — seeded and auto-populated
     * talkgroups are frequently named exactly that, and printing it again makes
     * every row look like it is stuttering.
     */
    private callRow(call: RdioScannerCall | null, previous: RdioScannerCall | null | undefined): SearchRow {
        if (!call) {
            return { kind: 'call', call, showDate: false, name: '', patches: '' };
        }

        const day = (value: unknown) => new Date(value as string).toDateString();

        const name = `${call.talkgroupData?.name ?? ''}`.trim();
        const label = `${call.talkgroupData?.label ?? call.talkgroup ?? ''}`.trim();
        const system = `${call.systemData?.label ?? call.system ?? ''}`.trim();

        const redundant = !name
            || name === label
            || name.toLowerCase() === `${system} ${label}`.toLowerCase();

        return {
            kind: 'call',
            call,
            showDate: !previous || day(previous.dateTime) !== day(call.dateTime),
            name: redundant ? '' : name,
            patches: this.patchLabels(call),
        };
    }

    /**
     * The talkgroups this transmission was also received on, as labels. These
     * are ids on the call's own system; one with no configured talkgroup shows
     * as the bare number rather than vanishing.
     */
    private patchLabels(call: RdioScannerCall): string {
        const patches = Array.isArray(call.patches) ? call.patches : [];

        if (!patches.length) {
            return '';
        }

        const talkgroups = this.config?.systems?.find((system) => system.id === call.system)?.talkgroups || [];

        return patches
            .filter((id) => id !== call.talkgroup)
            .map((id) => talkgroups.find((talkgroup) => talkgroup.id === id)?.label || `${id}`)
            .join(', ');
    }

    toggleGrouping(): void {
        this.groupByBurst = !this.groupByBurst;
        this.persistFilters();
    }

    toggleLiveUpdate(): void {
        this.liveUpdate = !this.liveUpdate;
        this.persistFilters();

        if (this.liveUpdate) {
            this.startLivePoll();
        } else {
            this.stopLivePoll();
        }
    }

    /**
     * True when new calls could actually reach this list.
     *
     * Oldest-first puts arrivals at the far end of a list nobody is looking
     * at, and a range that already closed cannot gain anything. Saying so is
     * better than a toggle that appears to work and never shows a call.
     */
    get liveUpdatable(): boolean {
        if (this.form.value.sort !== -1) {
            return false;
        }

        const stop = this.dateWindow().stop;

        return !stop || new Date(stop).getTime() >= Date.now();
    }

    private startLivePoll(): void {
        this.stopLivePoll();

        this.livePoll = setInterval(() => this.pollLive(), LIVE_POLL_MS);
    }

    private stopLivePoll(): void {
        if (this.livePoll) {
            clearInterval(this.livePoll);
            this.livePoll = undefined;
        }
    }

    /**
     * Asks for anything received since the list loaded.
     *
     * Held back while a search or a page is in flight — the service drops a
     * poll in that case anyway — and while the tab is hidden, so a forgotten
     * background tab is not asking the database a question every five seconds
     * for the rest of the day.
     */
    private pollLive(): void {
        if (!this.liveUpdate || !this.liveUpdatable || this.resultsPending) {
            return;
        }

        if (typeof document !== 'undefined' && document.hidden) {
            return;
        }

        // Only while the reader is at the top, where arrivals belong and where
        // adding them moves nothing they are looking at.
        if (this.viewport && this.viewport.measureScrollOffset() > LIVE_TOP_PX) {
            return;
        }

        this.rdioScannerService.searchCallsLive({
            ...this.buildOptions(),
            limit: LIVE_CHUNK_SIZE,
        });
    }

    trackRow(index: number, row: SearchRow): string {
        return row.kind === 'call' ? `c${row.call?.id ?? index}` : `${row.kind}${index}`;
    }

    toggleCallSelection(id: number): void {
        if (this.selectedCalls.has(id)) {
            this.selectedCalls.delete(id);
        } else {
            this.selectedCalls.add(id);
        }
    }

    isCallSelected(id: number): boolean {
        return this.selectedCalls.has(id);
    }

    selectAllVisible(): void {
        this.results.value.forEach((call) => {
            if (call?.id) {
                this.selectedCalls.add(call.id);
            }
        });
    }

    deselectAllVisible(): void {
        this.results.value.forEach((call) => {
            if (call?.id) {
                this.selectedCalls.delete(call.id);
            }
        });
    }

    areAllVisibleSelected(): boolean {
        const loaded = this.results.value.filter((call) => call?.id);
        if (loaded.length === 0) return false;
        return loaded.every((call) => this.selectedCalls.has(call.id));
    }

    areSomeVisibleSelected(): boolean {
        const loaded = this.results.value.filter((call) => call?.id);
        const selectedCount = loaded.filter((call) => this.selectedCalls.has(call.id)).length;
        return selectedCount > 0 && selectedCount < loaded.length;
    }

    toggleSelectAll(): void {
        if (this.areAllVisibleSelected()) {
            this.deselectAllVisible();
        } else {
            this.selectAllVisible();
        }
    }

    async downloadSelected(withTranscripts = false): Promise<void> {
        if (this.selectedCalls.size === 0 || this.isDownloading) return;

        this.isDownloading = true;

        // Oldest first, so the audio files and the rows of the manifest arrive
        // in the order the traffic happened rather than the order they were
        // clicked.
        const ids = this.results.value
            .filter((call): call is RdioScannerCall => !!call?.id && this.selectedCalls.has(call.id))
            .sort((a, b) => new Date(a.dateTime).getTime() - new Date(b.dateTime).getTime())
            .map((call) => call.id);

        const downloaded = await this.rdioScannerService.downloadMultiple(
            ids.length ? ids : Array.from(this.selectedCalls),
        );

        if (withTranscripts && downloaded.length) {
            this.downloadTranscriptManifest(downloaded);
        }

        this.isDownloading = false;
        this.selectedCalls.clear();
    }

    /**
     * Writes a CSV naming each downloaded file and what was said on it.
     *
     * The point is to make a folder of audio readable afterwards: on their own
     * the files are timestamps, and which talkgroup and which words went with
     * which file is exactly what is lost once they leave the app. Built from
     * what the download actually saved, so the filename column matches the
     * files on disk rather than what the client guessed they would be called.
     */
    private downloadTranscriptManifest(calls: RdioScannerCall[]): void {
        const header = ['File', 'Time', 'Talkgroup', 'Talkgroup name', 'Transcript'];

        const rows = calls.map((call) => [
            call.audioName ?? '',
            new Date(call.dateTime).toLocaleString(),
            `${call.talkgroupData?.label ?? call.talkgroup ?? ''}`,
            `${call.talkgroupData?.name ?? ''}`,
            (call.transcript ?? '').replace(/\s+/g, ' ').trim(),
        ]);

        // CRLF between rows: the RFC says so and Excel is unforgiving about it.
        const csv = [header, ...rows].map((row) => row.map(csvCell).join(',')).join('\r\n');

        // A BOM, because the overwhelmingly likely destination is Excel, which
        // reads a UTF-8 CSV as the local codepage without one and turns every
        // accented name and unit designator into mojibake.
        const blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8' });
        const url = URL.createObjectURL(blob);

        const el = document.createElement('a');
        el.style.display = 'none';
        el.href = url;
        el.download = `rdio-transcripts-${manifestStamp(new Date())}.csv`;

        document.body.appendChild(el);
        el.click();
        document.body.removeChild(el);

        // Freed on the next tick — revoking immediately races the click on
        // some browsers and the file arrives empty.
        setTimeout(() => URL.revokeObjectURL(url), 0);

        this.matSnackBar.open(
            `${calls.length} call${calls.length === 1 ? '' : 's'} downloaded, with transcripts`,
            '', { duration: 4000 });
    }

    getSelectedCount(): number {
        return this.selectedCalls.size;
    }

    async shareCall(id: number | undefined, event?: Event): Promise<void> {
        event?.stopPropagation();
        if (!id) return;
        const url = `${window.location.origin}${window.location.pathname}?call=${id}`;
        const title = 'Rdio Scanner call';

        // Copy to clipboard on every device. On touch devices also offer the
        // native share sheet afterwards so mobile users can forward the link.
        let copied = false;
        try {
            if (navigator.clipboard?.writeText) {
                await navigator.clipboard.writeText(url);
                copied = true;
            } else {
                copied = this.copyViaTextarea(url);
            }
        } catch {
            copied = false;
        }

        if (copied) {
            this.matSnackBar.open('Link copied to clipboard', '', { duration: 2000 });
        } else {
            this.matSnackBar.open(url, 'Dismiss', { duration: 6000 });
        }

        const isTouch = typeof window !== 'undefined'
            && typeof window.matchMedia === 'function'
            && window.matchMedia('(pointer: coarse)').matches;
        if (isTouch) {
            try {
                const nav: any = navigator;
                if (nav?.share) {
                    await nav.share({ title, url });
                }
            } catch {
                // user cancelled — clipboard copy already succeeded, nothing to do
            }
        }
    }

    async copyTranscript(transcript: string | undefined, event?: Event): Promise<void> {
        event?.stopPropagation();
        const text = (transcript ?? '').trim();
        if (!text) return;

        let copied = false;
        try {
            if (navigator.clipboard?.writeText) {
                await navigator.clipboard.writeText(text);
                copied = true;
            } else {
                copied = this.copyViaTextarea(text);
            }
        } catch {
            copied = false;
        }

        this.matSnackBar.open(copied ? 'Transcript copied to clipboard' : 'Unable to copy transcript', '', { duration: 2000 });
    }

    /**
     * The async clipboard API needs a secure context, so plain-http and older
     * browsers fall back to selecting a hidden textarea and running the legacy
     * copy command.
     */
    private copyViaTextarea(text: string): boolean {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        const copied = document.execCommand('copy');
        document.body.removeChild(ta);

        return copied;
    }

    // ------------------------------------------------------------------ events

    private applyConfig(config: RdioScannerConfig): void {
        this.config = config;

        this.time12h = config.time12hFormat || false;
        this.showRetranscribeButton = !!config.showRetranscribeButton;

        this.refreshOptions();
        this.rebuildChips();
    }

    private eventHandler(event: RdioScannerEvent): void {
        // A transcript finished on the server for a call this page is showing.
        //
        // Retranscribing is queued rather than answered inline, so this is how
        // the result arrives — the same push that delivers a transcript nobody
        // asked for by hand. The server sends it to every connected client, not
        // only those subscribed to that talkgroup, so the admin who asked gets
        // it whatever they happen to be listening to.
        if (event.transcriptReady) {
            const { id, transcript } = event.transcriptReady;

            this.clearTranscribeWatchdog(id);

            if (this.transcribingIds.delete(id)) {
                this.expandedTranscriptId = id;
            }

            this.applyTranscript(id, transcript);
        }

        if ('call' in event) {
            this.call = event.call;

            if (this.callPending) {
                this.callPending = undefined;
            }

            // If this call matches a pending deep-link focus, use its dateTime
            // to anchor the date filter and kick off a refined search that
            // brings the row within a chunk or two of the top.
            if (this.pendingFocusCallId && event.call?.id === this.pendingFocusCallId && event.call?.dateTime) {
                this.pendingFocusCallId = undefined;
                const dt = event.call.dateTime instanceof Date ? event.call.dateTime : new Date(event.call.dateTime);
                if (!isNaN(dt.getTime())) {
                    const day = new Date(dt.getFullYear(), dt.getMonth(), dt.getDate());
                    this.form.patchValue({ range: { start: day, end: day }, timeStart: '', timeStop: '' });
                    this.rebuildChips();
                    this.persistFilters();
                    this.deepLinkChunks = 0;
                    this.searchCalls();
                }
            }
        }

        if ('config' in event && event.config) {
            const wasFirstConfig = !this.config;

            this.callPending = undefined;

            this.applyConfig(event.config);

            // Pre-fetch the first results batch as soon as the initial CFG
            // lands so the search panel is already populated by the time the
            // user clicks SEARCH CALL. Skipped if a results pull is already
            // in flight (e.g. deep-link focus beat us to it).
            if (wasFirstConfig && !this.resultsPending && !this.playbackList) {
                this.searchCalls();
            }
        }

        if ('livefeedMode' in event) {
            this.livefeedOnline = event.livefeedMode === RdioScannerLivefeedMode.Online;

            this.livefeedPlayback = event.livefeedMode === RdioScannerLivefeedMode.Playback;
        }

        if ('playbackList' in event) {
            this.playbackList = event.playbackList;

            // The service owns the accumulation, so the component only ever
            // mirrors it. A new array identity is what tells the virtual
            // viewport its data length changed.
            this.results.next((this.playbackList?.results || []).slice());

            this.resultsPending = false;

            if (typeof event.searchExhausted === 'boolean') {
                this.exhausted = event.searchExhausted;
            }

            this.resolveHighlight();
        }

        if ('searchExhausted' in event && !('playbackList' in event)) {
            this.exhausted = !!event.searchExhausted;
        }

        if ('playbackPending' in event) {
            this.callPending = event.playbackPending;
        }

        if ('pause' in event) {
            this.paused = event.pause || false;
        }

        this.ngChangeDetectorRef.detectChanges();
    }

    /**
     * Deep links used to be resolved by jumping the paginator to the page the
     * call was on. With one accumulating list there is no page to jump to —
     * either the call is loaded, or another chunk is pulled until it is.
     */
    private resolveHighlight(): void {
        if (!this.highlightedCallId) {
            return;
        }

        const index = this.results.value.findIndex((call) => call.id === this.highlightedCallId);

        if (index >= 0) {
            this.scrollHighlightedIntoView(index);
            // Highlight fades after a short while so it doesn't stay stuck
            // forever if the user scrolls around.
            this.clearHighlightSoon();

            return;
        }

        if (!this.exhausted && this.deepLinkChunks < DEEP_LINK_MAX_CHUNKS) {
            this.deepLinkChunks += 1;
            this.loadMore();
        }
    }
}

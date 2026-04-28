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

import { HttpClient, HttpHeaders } from '@angular/common/http';
import { ChangeDetectorRef, Component, ElementRef, EventEmitter, OnDestroy, Output, ViewChild } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatPaginator } from '@angular/material/paginator';
import { MatSnackBar } from '@angular/material/snack-bar';
import { BehaviorSubject, firstValueFrom } from 'rxjs';
import {
    RdioScannerCall,
    RdioScannerConfig,
    RdioScannerEvent,
    RdioScannerLivefeedMode,
    RdioScannerPlaybackList,
    RdioScannerSearchOptions,
    RdioScannerSystem,
    RdioScannerTalkgroup,
} from '../rdio-scanner';
import { RdioScannerService } from '../rdio-scanner.service';

const ADMIN_TOKEN_STORAGE_KEY = 'rdio-scanner-admin-token';

@Component({
    selector: 'rdio-scanner-search',
    styleUrls: ['./search.component.scss'],
    templateUrl: './search.component.html',
})
export class RdioScannerSearchComponent implements OnDestroy {
    call: RdioScannerCall | undefined;
    callPending: number | undefined;

    form = this.ngFormBuilder.group({
        date: [null],
        group: [-1],
        q: [''],
        sort: [-1],
        system: [-1],
        tag: [-1],
        talkgroup: [-1],
    });

    private qDebounce: ReturnType<typeof setTimeout> | undefined;

    livefeedOnline = false;
    livefeedPlayback = false;

    playbackList: RdioScannerPlaybackList | undefined;

    optionsGroup: string[] = [];
    optionsSystem: string[] = [];
    optionsTag: string[] = [];
    optionsTalkgroup: string[] = [];

    paused = false;

    results = new BehaviorSubject(new Array<RdioScannerCall | null>(10));
    resultsPending = false;

    time12h = false;

    // Admin-gated retranscribe button. Controlled by the server via CFG.
    showRetranscribeButton = false;

    // Multi-select download state
    selectedCalls = new Set<number>();
    downloadMode = false;
    isDownloading = false;

    // Transcript expansion / retranscribe state
    expandedTranscriptId: number | undefined;
    transcribingIds = new Set<number>();

    // Deep-link focus state. When a user lands on ?call=<id>, we highlight
    // that call's row and scroll to it. Cleared on any user-driven form
    // change so normal searches don't carry the highlight.
    highlightedCallId: number | undefined;
    private pendingFocusCallId: number | undefined;
    private highlightClearTimer: ReturnType<typeof setTimeout> | undefined;

    @Output() focusedCall = new EventEmitter<number>();

    private config: RdioScannerConfig | undefined;

    private eventSubscription = this.rdioScannerService.event.subscribe((event: RdioScannerEvent) => this.eventHandler(event));

    private limit = 100;

    private offset = 0;

    @ViewChild(MatPaginator, { read: MatPaginator }) private paginator: MatPaginator | undefined;

    constructor(
        private rdioScannerService: RdioScannerService,
        private ngChangeDetectorRef: ChangeDetectorRef,
        private ngFormBuilder: FormBuilder,
        private ngHttpClient: HttpClient,
        private matSnackBar: MatSnackBar,
        private hostElement: ElementRef,
    ) { }

    isHighlighted(id: number | undefined): boolean {
        return !!id && this.highlightedCallId === id;
    }

    trackCall = (_i: number, row: RdioScannerCall | null): number | string => {
        return row?.id ?? _i;
    };

    focusCall(id: number): void {
        if (!id) return;

        this.highlightedCallId = id;
        this.pendingFocusCallId = id;

        if (this.highlightClearTimer) {
            clearTimeout(this.highlightClearTimer);
            this.highlightClearTimer = undefined;
        }

        // Kick playback and metadata fetch. The `call` event handler below
        // uses the returned dateTime to set the date filter, re-search, and
        // paginate/highlight. Playback runs alongside, which is fine — the
        // user asked for a clickable share link that takes them to the call.
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

    private toLocalDatetimeLocalString(d: Date): string {
        const pad = (n: number) => n.toString().padStart(2, '0');
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    }

    private scrollHighlightedIntoView(): void {
        if (!this.highlightedCallId) return;
        const host = this.hostElement?.nativeElement as HTMLElement | undefined;
        if (!host) return;
        setTimeout(() => {
            const row = host.querySelector(`[data-call-row="${this.highlightedCallId}"]`) as HTMLElement | null;
            if (row?.scrollIntoView) {
                row.scrollIntoView({ behavior: 'smooth', block: 'center' });
                this.focusedCall.emit(this.highlightedCallId);
            }
        }, 50);
    }

    isAdminAuthenticated(): boolean {
        return !!window?.sessionStorage?.getItem(ADMIN_TOKEN_STORAGE_KEY);
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
        return this.results.value.find((c) => c?.id === id) as RdioScannerCall | undefined;
    }

    private async loadTranscript(id: number): Promise<void> {
        const text = await this.rdioScannerService.fetchTranscript(id);
        this.applyTranscript(id, text);
    }

    private applyTranscript(id: number, transcript: string): void {
        const current = this.results.value.slice();
        const idx = current.findIndex((c) => c?.id === id);
        if (idx >= 0 && current[idx]) {
            current[idx] = { ...(current[idx] as RdioScannerCall), transcript, hasTranscript: !!transcript };
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

        const token = window?.sessionStorage?.getItem(ADMIN_TOKEN_STORAGE_KEY);
        if (!token) {
            this.matSnackBar.open('Sign in as admin to request a transcription.', '', { duration: 4000 });
            return;
        }

        this.transcribingIds.add(id);
        this.expandedTranscriptId = id;
        this.ngChangeDetectorRef.detectChanges();

        try {
            const url = `${window.location.href}/../api/admin/transcribe`;
            const res = await firstValueFrom(this.ngHttpClient.post<{ id: number; transcript: string }>(
                url,
                { id, manual: false },
                { headers: new HttpHeaders({ Authorization: token }), responseType: 'json' },
            ));

            this.applyTranscript(id, res.transcript);
        } catch (err: any) {
            const msg = err?.error?.error || err?.message || 'Transcription failed.';
            this.matSnackBar.open(msg, '', { duration: 5000 });
        } finally {
            this.transcribingIds.delete(id);
            this.ngChangeDetectorRef.detectChanges();
        }
    }

    download(id: number): void {
        this.rdioScannerService.loadAndDownload(id);
    }

    // Multi-select methods
    toggleDownloadMode(): void {
        this.downloadMode = !this.downloadMode;
        if (!this.downloadMode) {
            this.selectedCalls.clear();
        }
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
        const currentResults = this.results.value;
        currentResults.forEach(call => {
            if (call?.id) {
                this.selectedCalls.add(call.id);
            }
        });
    }

    deselectAllVisible(): void {
        const currentResults = this.results.value;
        currentResults.forEach(call => {
            if (call?.id) {
                this.selectedCalls.delete(call.id);
            }
        });
    }

    areAllVisibleSelected(): boolean {
        const currentResults = this.results.value.filter(call => call?.id);
        if (currentResults.length === 0) return false;
        return currentResults.every(call => call?.id && this.selectedCalls.has(call.id));
    }

    areSomeVisibleSelected(): boolean {
        const currentResults = this.results.value.filter(call => call?.id);
        const selectedCount = currentResults.filter(call => call?.id && this.selectedCalls.has(call.id)).length;
        return selectedCount > 0 && selectedCount < currentResults.length;
    }

    toggleSelectAll(): void {
        if (this.areAllVisibleSelected()) {
            this.deselectAllVisible();
        } else {
            this.selectAllVisible();
        }
    }

    async downloadSelected(): Promise<void> {
        if (this.selectedCalls.size === 0 || this.isDownloading) return;
        
        this.isDownloading = true;
        const ids = Array.from(this.selectedCalls);
        
        await this.rdioScannerService.downloadMultiple(ids);
        
        this.isDownloading = false;
        this.selectedCalls.clear();
    }

    getSelectedCount(): number {
        return this.selectedCalls.size;
    }

    formChangeHandler(): void {
        if (this.livefeedPlayback) {
            this.rdioScannerService.stopPlaybackMode();
        }

        this.paginator?.firstPage();

        this.refreshFilters();

        this.searchCalls();
    }

    ngOnDestroy(): void {
        this.eventSubscription.unsubscribe();
    }

    play(id: number): void {
        this.rdioScannerService.loadAndPlay(id);
    }

    refreshFilters(): void {
        if (!this.config) {
            return;
        }

        const selectedGroup = this.getSelectedGroup();
        const selectedSystem = this.getSelectedSystem();
        const selectedTag = this.getSelectedTag();
        const selectedTalkgroup = this.getSelectedTalkgroup();

        this.optionsSystem = this.config.systems
            .filter((system) => {
                const group = selectedGroup === undefined ||
                    system.talkgroups.some((talkgroup) => talkgroup.group === selectedGroup);
                const tag = selectedTag === undefined ||
                    system.talkgroups.some((talkgroup) => talkgroup.tag === selectedTag);
                return group && tag;
            })
            .map((system) => system.label);

        this.optionsTalkgroup = selectedSystem == undefined
            ? []
            : selectedSystem.talkgroups
                .filter((talkgroup) => {
                    const group = selectedGroup == undefined ||
                        talkgroup.group === selectedGroup;
                    const tag = selectedTag == undefined ||
                        talkgroup.tag === selectedTag;
                    return group && tag;
                })
                .map((talkgroup) => talkgroup.label);

        this.optionsGroup = Object.keys(this.config.groups)
            .filter((group) => {
                const system: boolean = selectedSystem === undefined ||
                    selectedSystem.talkgroups.some((talkgroup) => talkgroup.group === group)
                const talkgroup: boolean = selectedTalkgroup === undefined ||
                    selectedTalkgroup.group === group;
                const tag: boolean = selectedTag === undefined ||
                    (selectedTalkgroup !== undefined && selectedTalkgroup.tag === selectedTag) ||
                    (this.config !== undefined && this.config.systems
                        .flatMap((system) => system.talkgroups)
                        .some((talkgroup) => talkgroup.group === group && talkgroup.tag === selectedTag))
                return system && talkgroup && tag;
            })
            .sort((a, b) => a.localeCompare(b))

        this.optionsTag = Object.keys(this.config.tags)
            .filter((tag) => {
                const system: boolean = selectedSystem === undefined ||
                    selectedSystem.talkgroups.some((talkgroup) => talkgroup.tag === tag)
                const talkgroup: boolean = selectedTalkgroup === undefined ||
                    selectedTalkgroup.tag === tag;
                const group: boolean = selectedGroup === undefined ||
                    (selectedTalkgroup !== undefined && selectedTalkgroup.group === selectedGroup) ||
                    (this.config !== undefined && this.config.systems
                        .flatMap((system) => system.talkgroups)
                        .some((talkgroup) => talkgroup.tag === tag && talkgroup.group === selectedGroup))
                return system && talkgroup && group;
            })
            .sort((a, b) => a.localeCompare(b))

        this.form.patchValue({
            group: selectedGroup ? this.optionsGroup.findIndex((group) => group === selectedGroup) : -1,
            system: selectedSystem ? this.optionsSystem.findIndex((system) => system === selectedSystem.label) : -1,
            tag: selectedTag ? this.optionsTag.findIndex((tag) => tag === selectedTag) : -1,
            talkgroup: selectedTalkgroup ? this.optionsTalkgroup.findIndex((talkgroup) => talkgroup === selectedTalkgroup.label) : -1,
        });
    }

    refreshResults(): void {
        if (!this.paginator) {
            return;
        }

        const from = this.paginator.pageIndex * this.paginator.pageSize;

        const to = this.paginator.pageIndex * this.paginator.pageSize + this.paginator.pageSize - 1;

        if (!this.callPending && (from >= this.offset + this.limit || from < this.offset)) {
            this.searchCalls();

        } else if (this.playbackList) {
            const calls: Array<RdioScannerCall | null> = this.playbackList.results.slice(from % this.limit, to % this.limit + 1);

            while (calls.length < this.results.value.length) {
                calls.push(null);
            }

            this.results.next(calls);
        }
    }

    resetForm(): void {
        this.form.reset({
            date: null,
            group: -1,
            q: '',
            sort: -1,
            system: -1,
            tag: -1,
            talkgroup: -1,
        });

        this.paginator?.firstPage();

        this.formChangeHandler();
    }

    searchCalls(_force = false): void {
        const pageIndex = this.paginator?.pageIndex || 0;

        const pageSize = this.paginator?.pageSize || 0;

        this.offset = Math.floor((pageIndex * pageSize) / this.limit) * this.limit;

        const options: RdioScannerSearchOptions = {
            limit: this.limit,
            offset: this.offset,
            sort: this.form.value.sort,
        };

        if (typeof this.form.value.date === 'string') {
            options.date = new Date(Date.parse(this.form.value.date));
        }

        if (this.form.value.group >= 0) {
            const group = this.getSelectedGroup();

            if (group) {
                options.group = group;
            }
        }

        if (this.form.value.system >= 0) {
            const system = this.getSelectedSystem();

            if (system) {
                options.system = system.id;
            }
        }

        if (this.form.value.tag >= 0) {
            const tag = this.getSelectedTag();

            if (tag) {
                options.tag = tag;
            }
        }

        if (this.form.value.talkgroup >= 0) {
            const talkgroup = this.getSelectedTalkgroup();

            if (talkgroup) {
                options.talkgroup = talkgroup.id;
            }
        }

        const q = typeof this.form.value.q === 'string' ? this.form.value.q.trim() : '';
        if (q) {
            options.q = q;
        }

        this.resultsPending = true;

        // Intentionally NOT disabling the form here — locking the transcript
        // input out while a search is in flight blocks the user from typing
        // and causes debounced queries to swallow keystrokes. The progress
        // bar is already visible to signal pending state, and stale LCL
        // responses don't cause issues because the server keeps the most
        // recent result (newer responses overwrite older).
        this.rdioScannerService.searchCalls(options);
    }

    onQueryInput(): void {
        if (this.qDebounce) {
            clearTimeout(this.qDebounce);
        }
        this.qDebounce = setTimeout(() => {
            this.formChangeHandler();
        }, 600);
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
                const ta = document.createElement('textarea');
                ta.value = url;
                ta.style.position = 'fixed';
                ta.style.opacity = '0';
                document.body.appendChild(ta);
                ta.select();
                copied = document.execCommand('copy');
                document.body.removeChild(ta);
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

    stop(): void {
        if (this.livefeedPlayback) {
            this.rdioScannerService.stopPlaybackMode();

        } else {
            this.rdioScannerService.stop();
        }
    }

    private eventHandler(event: RdioScannerEvent): void {
        if ('call' in event) {
            this.call = event.call;

            // Deep-link focus has its own date-anchored re-search below.
            // Skip the in-page click-to-next-page navigation in that case
            // so we don't race two LCLs (one unfiltered, one filtered) on
            // the same WS — whichever response lands last overwrites
            // playbackList, and if the unfiltered one wins the deep-link
            // call isn't in the results and the highlight never resolves.
            const handlingDeepLink = !!(this.pendingFocusCallId
                && event.call?.id === this.pendingFocusCallId);

            if (this.callPending && !handlingDeepLink) {
                const index = this.results.value.findIndex((call) => call?.id === this.callPending);

                if (index === -1) {
                    if (this.form.value.sort === -1) {
                        this.paginator?.previousPage();

                    } else {
                        this.paginator?.nextPage();
                    }
                }

                this.callPending = undefined;
            } else if (this.callPending && handlingDeepLink) {
                // Still clear it so subsequent in-page navigation doesn't
                // think there's a pending pagination move queued up.
                this.callPending = undefined;
            }

            // If this call matches a pending deep-link focus, use its dateTime
            // to anchor the date filter and kick off a refined search that
            // will land the row in the visible page.
            if (this.pendingFocusCallId && event.call?.id === this.pendingFocusCallId && event.call?.dateTime) {
                this.pendingFocusCallId = undefined;
                const dt = event.call.dateTime instanceof Date ? event.call.dateTime : new Date(event.call.dateTime);
                if (!isNaN(dt.getTime())) {
                    this.form.patchValue({ date: this.toLocalDatetimeLocalString(dt) });
                    this.paginator?.firstPage();
                    this.searchCalls();
                }
            }
        }

        if ('config' in event) {
            const wasFirstConfig = !this.config;
            this.config = event.config;

            this.callPending = undefined;

            this.optionsGroup = Object.keys(this.config?.groups || []).sort((a, b) => a.localeCompare(b));
            this.optionsSystem = (this.config?.systems || []).map((system) => system.label);
            this.optionsTag = Object.keys(this.config?.tags || []).sort((a, b) => a.localeCompare(b));

            this.time12h = this.config?.time12hFormat || false;
            this.showRetranscribeButton = !!this.config?.showRetranscribeButton;

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

            this.refreshResults();

            this.resultsPending = false;
            // form.enable() no longer needed — we stopped disabling it on
            // search. Leaving the call out keeps focus/selection intact so
            // the user's cursor doesn't jump in the transcript input.

            if (this.highlightedCallId && this.playbackList && this.paginator) {
                const idx = this.playbackList.results.findIndex((c) => c.id === this.highlightedCallId);
                if (idx >= 0) {
                    const pageSize = this.paginator.pageSize || this.results.value.length || 10;
                    const targetPage = Math.floor(idx / pageSize);
                    if (this.paginator.pageIndex !== targetPage) {
                        this.paginator.pageIndex = targetPage;
                        this.refreshResults();
                    }
                    this.scrollHighlightedIntoView();
                    // Highlight fades after a short while so it doesn't stay
                    // stuck forever if the user scrolls around.
                    this.clearHighlightSoon();
                }
            }
        }

        if ('playbackPending' in event) {
            this.callPending = event.playbackPending;
        }

        if ('pause' in event) {
            this.paused = event.pause || false;
        }

        this.ngChangeDetectorRef.detectChanges();
    }

    private getSelectedGroup(): string | undefined {
        return this.optionsGroup[this.form.value.group];
    }

    private getSelectedSystem(): RdioScannerSystem | undefined {
        return this.config?.systems.find((system) => system.label === this.optionsSystem[this.form.value.system]);
    }

    private getSelectedTag(): string | undefined {
        return this.optionsTag[this.form.value.tag];
    }

    private getSelectedTalkgroup(): RdioScannerTalkgroup | undefined {
        const system = this.getSelectedSystem();

        return system
            ? system.talkgroups.find((talkgroup) => talkgroup.label === this.optionsTalkgroup[this.form.value.talkgroup])
            : undefined;
    }
}

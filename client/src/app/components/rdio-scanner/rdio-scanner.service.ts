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
import { EventEmitter, Inject, Injectable, OnDestroy } from '@angular/core';
import { Router } from '@angular/router';
import { interval, Subscription, timer } from 'rxjs';
import { takeWhile } from 'rxjs/operators';
import { AppUpdateService } from '../../shared/update/update.service';
import {
    RdioScannerAvoidOptions,
    RdioScannerBeepStyle,
    RdioScannerCall,
    RdioScannerCategory,
    RdioScannerCategoryStatus,
    RdioScannerCategoryType,
    RdioScannerConfig,
    RdioScannerEvent,
    RdioScannerLivefeed,
    RdioScannerLivefeedMap,
    RdioScannerLivefeedMode,
    RdioScannerPlaybackList,
    RdioScannerPreset,
    RdioScannerPresetExport,
    RdioScannerSearchOptions,
} from './rdio-scanner';

declare global {
    interface Window {
        webkitAudioContext: typeof AudioContext;
    }
}

enum WebsocketCallFlag {
    Download = 'd',
    Play = 'p',
}

enum WebsocketCommand {
    Call = 'CAL',
    Config = 'CFG',
    Expired = 'XPR',
    ListCall = 'LCL',
    ListenersCount = 'LSC',
    LivefeedMap = 'LFM',
    Max = 'MAX',
    Pin = 'PIN',
    Transcript = 'TRX',
    Version = 'VER',
}

// Messages exchanged over the stream-sync BroadcastChannel. 'request' asks the
// leader to (re)broadcast; 'state' carries the leader's effective selection.
interface StreamSyncMessage {
    type?: string;
    map?: { [key: number]: { [key: number]: boolean } };
    autoJump?: boolean;
    autoJumpThreshold?: number;
}

@Injectable()
export class RdioScannerService implements OnDestroy {
    static LOCAL_STORAGE_KEY_LEGACY = 'rdio-scanner';
    static LOCAL_STORAGE_KEY_LFM = 'rdio-scanner-lfm';
    static LOCAL_STORAGE_KEY_PIN = 'rdio-scanner-pin';
    // BroadcastChannel name used to live-sync the effective talkgroup
    // selection / avoid / hold state + auto-jump from a "leader" window
    // (the normal main page) to one or more "follower" windows (the /stream
    // OBS page). See enableFollowerMode()/broadcastLivefeedState() below.
    static STREAM_SYNC_CHANNEL = 'rdio-scanner-stream-sync';

    event = new EventEmitter<RdioScannerEvent>();

    // Stream live-sync. A follower (the /stream page) opts in via
    // enableFollowerMode(): it stops broadcasting its own state and instead
    // applies whatever the leader broadcasts. Leaders broadcast their
    // effective livefeed map + auto-jump whenever those change. Live Feed
    // on/off is deliberately NOT synced — the stream owns its own playback.
    private followerMode = false;
    private syncChannel: BroadcastChannel | undefined;

    private transcriptResolvers = new Map<number, (text: string) => void>();

    // Deep-link call ID parsed from ?call=<id> at construction time. Exposed
    // via consumePendingDeepLink() so the top-level component can pick it up
    // from ngAfterViewInit if it subscribed too late to catch the event emit
    // (possible now that the index.html early-WS can land CFG before the
    // component's subscription is attached).
    private pendingDeepLinkCallId: number | undefined;

    consumePendingDeepLink(): number | undefined {
        const id = this.pendingDeepLinkCallId;
        this.pendingDeepLinkCallId = undefined;
        return id;
    }

    // Tracks the latest linked state independently of the EventEmitter so
    // late subscribers (components that mount after the WS has already
    // opened — common with the index.html early-WS landing CFG before
    // Angular bootstraps) can pull the current value via isLinked instead
    // of waiting forever for a `{linked:true}` that already fired into the
    // void.
    private linkedState = false;
    get isLinked(): boolean {
        return this.linkedState;
    }

    // Same pattern as linkedState/isLinked above, applied to the three
    // pieces of state that arrive via the CFG/categories/map event
    // emissions. Late-mounted panels (search, select, stats) can seed
    // their local copy via these getters in ngOnInit and avoid an empty
    // initial render if the events fired before they subscribed.
    private configState: RdioScannerConfig | undefined;
    getConfig(): RdioScannerConfig | undefined {
        return this.configState;
    }

    private categoriesState: RdioScannerCategory[] | undefined;
    getCategories(): RdioScannerCategory[] | undefined {
        return this.categoriesState;
    }

    private livefeedMapState: RdioScannerLivefeedMap | undefined;
    getLivefeedMap(): RdioScannerLivefeedMap | undefined {
        return this.livefeedMapState;
    }

    // When true, live-feed calls without a transcript are held OUT of the
    // playback queue entirely until their transcript arrives (or a timeout
    // releases them so nothing is lost). Admin-controlled — flows in via
    // the server's CFG broadcast.
    //
    // Stored as an ordered array (not a Map) and drained head-of-line via
    // drainPendingHead() so calls go to the main queue in arrival order,
    // never transcript-arrival order. A slow-Whisper call at the head holds
    // back the tail until its transcript arrives or its timeout fires.
    private waitForTranscript = false;
    private pendingTranscriptCalls: Array<{
        id: number;
        call: RdioScannerCall;
        priority: boolean;
        ready: boolean;
        // Timers are absent for calls that arrived with a transcript already
        // attached — they enter the pre-queue marked ready so head-of-line
        // ordering still applies, but there's no polling/timeout to set up.
        fetchTimer?: ReturnType<typeof setTimeout>;
        timeoutTimer?: ReturnType<typeof setTimeout>;
    }> = [];
    // Wait at most 20s for a held call's transcript before releasing the call
    // anyway. If the transcript eventually shows up via a server WS push, it
    // still gets applied to the call in-place — see applyLateTranscript().
    private static readonly TRANSCRIPT_WAIT_MAX_MS = 20000;
    private static readonly TRANSCRIPT_WAIT_POLL_MS = 2000;

    private audioContext: AudioContext | undefined;

    private audioSource: AudioBufferSourceNode | undefined;
    private audioSourceStartTime = NaN;
    private gainNode: GainNode | undefined;

    // Bumped every time playback starts or stops. decodeAudioData is async —
    // if the user switches calls while a previous decode is in flight, the
    // stale callback must discard itself instead of starting the wrong buffer.
    private playGeneration = 0;

    private beepContext: AudioContext | undefined;

    private volume = 1.0;
    private muted = false;

    private call: RdioScannerCall | undefined;
    private callPrevious: RdioScannerCall | undefined;
    private callQueue: RdioScannerCall[] = [];

    // Cache of decoded call durations (id -> seconds), used to compute the
    // combined "queue delay" shown on the LCD. Filled lazily by decoding each
    // queued call's audio once. Bounded — pruned if it grows past 512 entries.
    private callDurations = new Map<number, number>();
    // Dedicated context for duration decoding. Falls back to this when the
    // playback audioContext doesn't exist yet (cold page, pre-gesture). A
    // BaseAudioContext can decodeAudioData while suspended, so no gesture is
    // needed just to measure durations.
    private decodeContext: AudioContext | undefined;

    // User toggle: when on, the live-feed queue auto-jumps ahead (drops the
    // oldest queued calls) whenever the combined queue delay exceeds the
    // threshold, keeping a buffer no longer than the threshold so a listener
    // who fell behind catches back up toward live. The threshold is
    // user-adjustable (1-10 minutes) via the LCD slider.
    private autoJumpAhead = false;
    private autoJumpThresholdMin = RdioScannerService.QUEUE_AUTO_JUMP_DEFAULT_MIN;
    private static readonly QUEUE_AUTO_JUMP_DEFAULT_MIN = 5;
    private static readonly QUEUE_AUTO_JUMP_MIN_MIN = 1;
    private static readonly QUEUE_AUTO_JUMP_MAX_MIN = 10;

    private categories: RdioScannerCategory[] = [];

    private config: RdioScannerConfig = {
        dimmerDelay: false,
        groups: {},
        keypadBeeps: false,
        playbackGoesLive: false,
        showListenersCount: false,
        systems: [],
        tags: {},
        tagsToggle: false,
        time12hFormat: false,
    };

    private instanceId = 'default';

    private livefeedMap = {} as RdioScannerLivefeedMap;
    private livefeedMapPriorToHoldSystem: RdioScannerLivefeedMap | undefined;
    private livefeedMapPriorToHoldTalkgroup: RdioScannerLivefeedMap | undefined;
    private livefeedMode = RdioScannerLivefeedMode.Offline;
    private livefeedPaused = false;

    private playbackList: RdioScannerPlaybackList | undefined;
    private playbackPending: number | undefined;
    private playbackRefreshing = false;

    private skipDelay: Subscription | undefined;

    private websocket: WebSocket | undefined;

    constructor(
        appUpdateService: AppUpdateService,
        private router: Router,
        @Inject(DOCUMENT) private document: Document,
    ) {
        this.loadVolumeSettings();
        this.loadAutoJumpSetting();
        this.bootstrapAudio();

        this.initializeInstanceId();

        this.readLivefeedMap();


        try {
            // Read the query param straight from window.location — router.url
            // isn't populated yet at service-construction time, so the earlier
            // parseUrl path could miss the ?call=<id> from share links.
            const search = (typeof window !== 'undefined' && window.location && window.location.search) || '';
            const raw = new URLSearchParams(search).get('call');
            const n = raw ? parseInt(raw, 10) : NaN;
            if (Number.isFinite(n) && n > 0) {
                this.pendingDeepLinkCallId = n;
            }
        } catch {
            // ignore
        }

        this.openWebsocket();

        this.initStreamSync();
    }

    authenticate(password: string): void {
        this.sendtoWebsocket(WebsocketCommand.Pin, window.btoa(password));
    }

    avoid(options: RdioScannerAvoidOptions = {}): void {
        const clearTimer = (lfm: RdioScannerLivefeed): void => {
            lfm.minutes = undefined;
            lfm.timer?.unsubscribe();
            lfm.timer = undefined;
        };

        const setTimer = (lfm: RdioScannerLivefeed, minutes: number): void => {
            lfm.minutes = minutes;
            lfm.timer = timer(minutes * 60 * 1000).subscribe(() => {
                lfm.active = true;
                lfm.minutes = undefined;
                lfm.timer = undefined;

                this.rebuildCategories();
                this.saveLivefeedMap();

                this.event.emit({
                    categories: this.categories,
                    map: this.livefeedMap,
                });
            });
        };

        if (this.livefeedMapPriorToHoldSystem) {
            this.livefeedMapPriorToHoldSystem = undefined;
        }

        if (this.livefeedMapPriorToHoldTalkgroup) {
            this.livefeedMapPriorToHoldTalkgroup = undefined;
        }

        if (typeof options.all === 'boolean') {
            Object.keys(this.livefeedMap).map((sys: string) => +sys).forEach((sys: number) => {
                Object.keys(this.livefeedMap[sys]).map((tg: string) => +tg).forEach((tg: number) => {
                    const lfm = this.livefeedMap[sys][tg];
                    clearTimer(lfm);
                    lfm.active = typeof options.status === 'boolean' ? options.status : !!options.all;
                });
            });

        } else if (options.call) {
            const lfm = this.livefeedMap[options.call.system][options.call.talkgroup];
            clearTimer(lfm);
            lfm.active = typeof options.status === 'boolean' ? options.status : !lfm.active;
            if (typeof options.minutes === 'number') setTimer(lfm, options.minutes);

        } else if (options.system && options.talkgroup) {
            const lfm = this.livefeedMap[options.system.id][options.talkgroup.id];
            clearTimer(lfm);
            lfm.active = typeof options.status === 'boolean' ? options.status : !lfm.active;
            if (typeof options.minutes === 'number') setTimer(lfm, options.minutes);

        } else if (options.system && !options.talkgroup) {
            const sys = options.system.id;
            Object.keys(this.livefeedMap[sys]).map((tg: string) => +tg).forEach((tg: number) => {
                const lfm = this.livefeedMap[sys][tg];
                clearTimer(lfm);
                lfm.active = typeof options.status === 'boolean' ? options.status : !lfm.active;
            });

        } else {
            const call = this.call || this.callPrevious;
            if (call) {
                const lfm = this.livefeedMap[call.system][call.talkgroup];
                clearTimer(lfm);
                lfm.active = typeof options.status === 'boolean' ? options.status : !lfm.active;
                if (typeof options.minutes === 'number') setTimer(lfm, options.minutes);
            }
        }

        if (this.livefeedMode !== RdioScannerLivefeedMode.Playback) {
            this.cleanQueue();
        }

        this.rebuildCategories();

        this.saveLivefeedMap();

        if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
            this.startLivefeed();
        }

        this.event.emit({
            categories: this.categories,
            holdSys: false,
            holdTg: false,
            map: this.livefeedMap,
            queue: this.callQueue.length,
            queueTime: this.computeDelay(),
        });
    }

    beep(style = RdioScannerBeepStyle.Activate): Promise<void> {
        return new Promise((resolve) => {
            const context = this.beepContext;

            const seq = this.config.keypadBeeps && this.config.keypadBeeps[style];

            if (!context || !seq) {
                resolve();

                return;
            }

            const gn = context.createGain();

            gn.gain.value = .04 * RdioScannerService.MAX_GAIN;

            gn.connect(context.destination);

            seq.forEach((beep, index) => {
                const osc = context.createOscillator();

                osc.connect(gn);

                osc.frequency.value = beep.frequency;

                osc.type = beep.type;

                if (index === seq.length - 1) {
                    osc.onended = () => resolve();
                }

                osc.start(context.currentTime + beep.begin);

                osc.stop(context.currentTime + beep.end);
            });
        });
    }

    private getCallAlertName(call: RdioScannerCall): string | undefined {
        const systems = this.config?.systems;
        if (!systems) {
            return undefined;
        }
        const system = systems.find((s) => s.id === call.system);
        const talkgroup = system?.talkgroups?.find((t) => t.id === call.talkgroup);
        // Talkgroup alert wins; fall back to system-level alert. Matches
        // upstream v7 precedence.
        return talkgroup?.alert || system?.alert || undefined;
    }

    // Schedules an alert preset on the playback audio context starting at
    // `startTime` and returns its total duration in seconds. Caller can use
    // the duration to schedule the call audio source to start right after.
    private scheduleAlert(name: string, startTime: number): number {
        const context = this.audioContext;
        const seq = this.config?.alerts?.[name];
        if (!context || !seq || seq.length === 0) {
            return 0;
        }
        const gn = context.createGain();
        gn.gain.value = 0.04 * RdioScannerService.MAX_GAIN;
        gn.connect(context.destination);
        let maxEnd = 0;
        for (const beep of seq) {
            const osc = context.createOscillator();
            osc.connect(gn);
            osc.frequency.value = beep.frequency;
            osc.type = beep.type;
            osc.start(startTime + beep.begin);
            osc.stop(startTime + beep.end);
            if (beep.end > maxEnd) {
                maxEnd = beep.end;
            }
        }
        return maxEnd;
    }

    clearPin(): void {
        window?.localStorage.removeItem(RdioScannerService.LOCAL_STORAGE_KEY_PIN);
    }

    ngOnDestroy(): void {
        this.closeWebsocket();

        this.stop();

        try {
            this.syncChannel?.close();
        } catch (_) {
            //
        }
    }

    // ---------------------------------------------------------------------
    // Stream live-sync (leader/follower over BroadcastChannel)
    // ---------------------------------------------------------------------

    private initStreamSync(): void {
        try {
            if (typeof BroadcastChannel === 'undefined') {
                return;
            }

            this.syncChannel = new BroadcastChannel(RdioScannerService.STREAM_SYNC_CHANNEL);

            this.syncChannel.onmessage = (e: MessageEvent) => this.onStreamSyncMessage(e?.data);

        } catch (_) {
            // BroadcastChannel unavailable (old browser / SSR) — sync is a
            // best-effort enhancement, so silently degrade.
        }
    }

    // Called by the /stream page. Turns this service instance into a
    // follower: it no longer broadcasts and starts applying leader state.
    enableFollowerMode(): void {
        this.followerMode = true;
    }

    // Restores leader behaviour — called when the /stream page is torn down so
    // a same-tab navigation back to the main page resumes broadcasting.
    disableFollowerMode(): void {
        this.followerMode = false;
    }

    // Asks the leader window to (re)broadcast its current state — used by a
    // freshly-opened /stream page to immediately pull holds/selection that
    // were set before it opened (holds aren't persisted to localStorage).
    requestSyncState(): void {
        try {
            this.syncChannel?.postMessage({ type: 'request' });
        } catch (_) {
            //
        }
    }

    private onStreamSyncMessage(msg: StreamSyncMessage): void {
        if (!msg || typeof msg !== 'object') {
            return;
        }

        if (msg.type === 'request') {
            // Only a leader answers a follower's request for current state.
            if (!this.followerMode) {
                this.broadcastLivefeedState();
            }
            return;
        }

        if (msg.type === 'state') {
            // Only followers apply incoming state; leaders ignore it.
            if (this.followerMode) {
                this.applyFollowerState(msg);
            }
        }
    }

    // Reduces the in-memory livefeedMap to a plain {sys:{tg:active}} object —
    // the same shape startLivefeed() sends to the server.
    private buildActiveMap(): { [key: number]: { [key: number]: boolean } } {
        return Object.keys(this.livefeedMap).reduce((sysMap: { [key: number]: { [key: number]: boolean } }, sys) => {
            sysMap[+sys] = Object.keys(this.livefeedMap[+sys]).reduce((tgMap: { [key: number]: boolean }, tg) => {
                tgMap[+tg] = this.livefeedMap[+sys][+tg].active;
                return tgMap;
            }, {});
            return sysMap;
        }, {});
    }

    // Broadcasts the leader's effective selection + auto-jump to followers.
    // No-op on followers (they never lead) and when BroadcastChannel is
    // unavailable. Note BroadcastChannel never echoes back to the sender, so
    // a leader never receives its own state.
    broadcastLivefeedState(): void {
        if (this.followerMode || !this.syncChannel) {
            return;
        }

        try {
            this.syncChannel.postMessage({
                type: 'state',
                map: this.buildActiveMap(),
                autoJump: this.autoJumpAhead,
                autoJumpThreshold: this.autoJumpThresholdMin,
            });
        } catch (_) {
            //
        }
    }

    // Follower path: mirror the leader's talkgroup selection / avoid / hold
    // result + auto-jump, then resubscribe so the server sends the same set
    // of calls. Deliberately does not touch livefeedMode — the stream keeps
    // playing regardless of whether the leader's Live Feed is on or off.
    private applyFollowerState(msg: StreamSyncMessage): void {
        if (typeof msg.autoJump === 'boolean') {
            this.autoJumpAhead = msg.autoJump;
            this.event.emit({ autoJumpAhead: this.autoJumpAhead });
        }

        if (typeof msg.autoJumpThreshold === 'number') {
            this.autoJumpThresholdMin = msg.autoJumpThreshold;
            this.event.emit({ autoJumpThreshold: this.autoJumpThresholdMin });
        }

        const incoming = msg.map || {};

        Object.keys(incoming).forEach((sys) => {
            Object.keys(incoming[+sys]).forEach((tg) => {
                if (!this.livefeedMap[+sys]) {
                    this.livefeedMap[+sys] = {};
                }
                if (!this.livefeedMap[+sys][+tg]) {
                    this.livefeedMap[+sys][+tg] = { active: incoming[+sys][+tg] } as RdioScannerLivefeed;
                } else {
                    this.livefeedMap[+sys][+tg].active = incoming[+sys][+tg];
                }
            });
        });

        this.livefeedMapState = this.livefeedMap;

        // Persist so a stream reload restores the last mirrored selection.
        // saveLivefeedMap() also calls broadcastLivefeedState(), which is a
        // no-op here because followerMode is set.
        this.saveLivefeedMap();

        this.rebuildCategories();

        this.event.emit({ categories: this.categories, map: this.livefeedMap });

        if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
            this.startLivefeed();
        }
    }

    holdSystem(options?: { resubscribe?: boolean }): void {
        const call = this.call || this.callPrevious;

        if (call && this.livefeedMap) {
            if (this.livefeedMapPriorToHoldSystem) {
                this.livefeedMap = this.livefeedMapPriorToHoldSystem;

                this.livefeedMapPriorToHoldSystem = undefined;

            } else {
                if (this.livefeedMapPriorToHoldTalkgroup) {
                    this.holdTalkgroup({ resubscribe: false });
                }

                this.livefeedMapPriorToHoldSystem = this.livefeedMap;

                this.livefeedMap = Object.keys(this.livefeedMap).map((sys) => +sys).reduce((sysMap, sys) => {
                    const allOn = Object.keys(this.livefeedMap[sys]).map((tg) => +tg).every((tg) => !this.livefeedMap[sys][tg]);

                    sysMap[sys] = Object.keys(this.livefeedMap[sys]).map((tg) => +tg).reduce((tgMap, tg) => {
                        this.livefeedMap[sys][tg].timer?.unsubscribe();

                        tgMap[tg] = {
                            active: sys === call.system ? allOn || this.livefeedMap[sys][tg].active : false,
                        } as RdioScannerLivefeed;

                        return tgMap;
                    }, {} as { [key: number]: RdioScannerLivefeed });

                    return sysMap;
                }, {} as RdioScannerLivefeedMap);

                this.cleanQueue();
            }

            this.rebuildCategories();

            if (typeof options?.resubscribe !== 'boolean' || options.resubscribe) {
                if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
                    this.startLivefeed();
                }
            }

            this.event.emit({
                categories: this.categories,
                holdSys: !!this.livefeedMapPriorToHoldSystem,
                holdTg: false,
                map: this.livefeedMap,
                queue: this.callQueue.length,
                queueTime: this.computeDelay(),
            });

            // Mirror the hold result to /stream followers even when offline
            // (online holds already broadcast via startLivefeed's resubscribe).
            this.broadcastLivefeedState();
        }
    }

    holdTalkgroup(options?: { resubscribe?: boolean }): void {
        const call = this.call || this.callPrevious;

        if (call && this.livefeedMap) {
            if (this.livefeedMapPriorToHoldTalkgroup) {
                this.livefeedMap = this.livefeedMapPriorToHoldTalkgroup;

                this.livefeedMapPriorToHoldTalkgroup = undefined;

            } else {
                if (this.livefeedMapPriorToHoldSystem) {
                    this.holdSystem({ resubscribe: false });
                }

                this.livefeedMapPriorToHoldTalkgroup = this.livefeedMap;

                this.livefeedMap = Object.keys(this.livefeedMap).map((sys) => +sys).reduce((sysMap, sys) => {
                    sysMap[sys] = Object.keys(this.livefeedMap[sys]).map((tg) => +tg).reduce((tgMap, tg) => {
                        this.livefeedMap[sys][tg].timer?.unsubscribe();

                        tgMap[tg] = {
                            active: sys === call.system ? tg === call.talkgroup : false,
                        } as RdioScannerLivefeed;

                        return tgMap;
                    }, {} as { [key: number]: RdioScannerLivefeed });

                    return sysMap;
                }, {} as RdioScannerLivefeedMap);

                this.cleanQueue();
            }

            this.rebuildCategories();

            if (typeof options?.resubscribe !== 'boolean' || options.resubscribe) {
                if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
                    this.startLivefeed();
                }
            }

            this.event.emit({
                categories: this.categories,
                holdSys: false,
                holdTg: !!this.livefeedMapPriorToHoldTalkgroup,
                map: this.livefeedMap,
                queue: this.callQueue.length,
                queueTime: this.computeDelay(),
            });

            // Mirror the hold result to /stream followers even when offline
            // (online holds already broadcast via startLivefeed's resubscribe).
            this.broadcastLivefeedState();
        }
    }

    isAvoided(call: RdioScannerCall): boolean {
        return !!this.livefeedMap[call.system] && this.livefeedMap[call.system][call.talkgroup]?.active !== true;
    }

    isAvoidedTimer(call: RdioScannerCall): number {
        if (!!this.livefeedMap[call.system] && this.livefeedMap[call.system][call.talkgroup]?.minutes !== undefined) {
            return this.livefeedMap[call.system][call.talkgroup]?.minutes || 0;
        }
        return 0;
    }

    isPatched(call: RdioScannerCall): boolean {
        // A call is "patched" whenever the recorder reported one or more
        // patched-talkgroup IDs alongside it — that's information the LCD
        // should always surface so the user can see at a glance that this
        // traffic is bridged across talkgroups. The previous definition
        // only fired when the user had explicitly avoided the source TG
        // AND the patch routed it back in — useful only in that narrow
        // case, and invisible for users who haven't avoided anything.
        return Array.isArray(call.patches) && call.patches.length > 0;
    }

    livefeed(): void {
        if (this.livefeedMode === RdioScannerLivefeedMode.Offline) {
            this.startLivefeed();

        } else if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
            this.stopLivefeed();

        } else if (this.livefeedMode === RdioScannerLivefeedMode.Playback) {
            this.stopPlaybackMode();
        }
    }

    loadAndDownload(id: number): void {
        if (!id) {
            return;
        }

        this.getCall(id, WebsocketCallFlag.Download);
    }

    async downloadMultiple(ids: number[]): Promise<void> {
        if (!ids || ids.length === 0) {
            return;
        }

        // Download calls sequentially with a small delay to avoid overwhelming the server
        for (const id of ids) {
            this.getCall(id, WebsocketCallFlag.Download);
            // Small delay between downloads to ensure browser handles each download
            await new Promise(resolve => setTimeout(resolve, 300));
        }
    }

    loadAndPlay(id: number): void {
        if (!id) {
            return;
        }

        this.trackUmamiEvent('call-play', { callId: id });

        if (this.skipDelay) {
            this.skipDelay.unsubscribe();

            this.skipDelay = undefined;
        }

        this.playbackPending = id;

        this.stop();

        if (this.livefeedMode === RdioScannerLivefeedMode.Offline) {
            this.livefeedMode = RdioScannerLivefeedMode.Playback;

            if (this.livefeedMapPriorToHoldSystem) {
                this.holdSystem({ resubscribe: false });
            }

            if (this.livefeedMapPriorToHoldTalkgroup) {
                this.holdTalkgroup({ resubscribe: false });
            }

            this.event.emit({ livefeedMode: this.livefeedMode, playbackPending: id });

        } else if (this.livefeedMode === RdioScannerLivefeedMode.Playback) {
            this.event.emit({ playbackPending: id });
        }

        this.getCall(id, WebsocketCallFlag.Play);
    }

    pause(status = !this.livefeedPaused): void {
        this.livefeedPaused = status;

        if (status) {
            this.audioContext?.suspend();

        } else {
            this.audioContext?.resume();

            this.play();
        }

        this.event.emit({ pause: this.livefeedPaused });
    }

    setVolume(volume: number): void {
        this.volume = Math.max(0, Math.min(1, volume));
        this.updateGainNode();
        this.saveVolumeSettings();
        this.event.emit({ volume: this.volume, muted: this.muted });
    }

    setMute(muted: boolean): void {
        this.muted = muted;
        this.updateGainNode();
        this.saveVolumeSettings();
        this.event.emit({ volume: this.volume, muted: this.muted });
    }

    getVolume(): number {
        return this.volume;
    }

    isMuted(): boolean {
        return this.muted;
    }

    setAutoJumpAhead(enabled: boolean): void {
        this.autoJumpAhead = enabled;
        this.saveAutoJumpSetting();

        if (enabled) {
            this.maybeAutoJumpAhead();
        }

        this.event.emit({
            autoJumpAhead: this.autoJumpAhead,
            autoJumpThreshold: this.autoJumpThresholdMin,
            queue: this.livefeedMode === RdioScannerLivefeedMode.Playback
                ? this.getPlaybackQueueCount()
                : this.callQueue.length,
            queueTime: this.computeDelay(),
        });
    }

    isAutoJumpAhead(): boolean {
        return this.autoJumpAhead;
    }

    setAutoJumpThresholdMinutes(minutes: number): void {
        const clamped = Math.max(
            RdioScannerService.QUEUE_AUTO_JUMP_MIN_MIN,
            Math.min(RdioScannerService.QUEUE_AUTO_JUMP_MAX_MIN, Math.round(minutes)),
        );

        this.autoJumpThresholdMin = clamped;
        this.saveAutoJumpSetting();

        // A lower threshold may now put us over the line — re-evaluate.
        this.maybeAutoJumpAhead();

        this.event.emit({
            autoJumpAhead: this.autoJumpAhead,
            autoJumpThreshold: this.autoJumpThresholdMin,
            queue: this.livefeedMode === RdioScannerLivefeedMode.Playback
                ? this.getPlaybackQueueCount()
                : this.callQueue.length,
            queueTime: this.computeDelay(),
        });
    }

    getAutoJumpThresholdMinutes(): number {
        return this.autoJumpThresholdMin;
    }

    private loadAutoJumpSetting(): void {
        try {
            this.autoJumpAhead = window?.localStorage?.getItem('rdio-scanner-auto-jump') === '1';

            const storedThreshold = window?.localStorage?.getItem('rdio-scanner-auto-jump-threshold');
            const parsed = storedThreshold != null ? parseInt(storedThreshold, 10) : NaN;
            if (!isNaN(parsed)) {
                this.autoJumpThresholdMin = Math.max(
                    RdioScannerService.QUEUE_AUTO_JUMP_MIN_MIN,
                    Math.min(RdioScannerService.QUEUE_AUTO_JUMP_MAX_MIN, parsed),
                );
            }
        } catch (e) {
            // Ignore storage errors
        }
    }

    private saveAutoJumpSetting(): void {
        try {
            window?.localStorage?.setItem('rdio-scanner-auto-jump', this.autoJumpAhead ? '1' : '0');
            window?.localStorage?.setItem('rdio-scanner-auto-jump-threshold', `${this.autoJumpThresholdMin}`);
        } catch (e) {
            // Ignore storage errors
        }

        // Keep /stream followers' auto-jump in step. No-op on followers.
        this.broadcastLivefeedState();
    }

    // computeQueueTime sums the cached durations of every call currently in the
    // live-feed queue. Calls whose duration isn't cached yet are kicked off for
    // a lazy decode (and counted as 0 until it lands), so the total only ever
    // under-reports transiently and self-corrects once decodes complete.
    private computeQueueTime(): number {
        let total = 0;

        for (const call of this.callQueue) {
            if (!call?.id) {
                continue;
            }

            const duration = this.callDurations.get(call.id);

            if (duration != null) {
                total += duration;
            } else {
                this.ensureCallDuration(call);
            }
        }

        return total;
    }

    // remainingCurrentCallSec returns how much of the currently-playing call is
    // left to play (0 when nothing is playing or its length isn't known yet).
    // Before the first playback tick sets audioSourceStartTime we treat the
    // position as 0, so a call that just started counts as its full length
    // rather than blipping in a beat late.
    private remainingCurrentCallSec(): number {
        if (!this.call?.id || !this.audioContext) {
            return 0;
        }

        const duration = this.callDurations.get(this.call.id);
        if (duration == null || duration <= 0) {
            return 0;
        }

        const position = isNaN(this.audioSourceStartTime)
            ? 0
            : this.audioContext.currentTime - this.audioSourceStartTime;

        return Math.max(0, duration - position);
    }

    // computeDelay is the full "how far behind live" figure shown on the LCD:
    // the combined length of everything queued plus whatever's left of the call
    // playing right now. It ticks down second-by-second as the current call
    // plays (driven by the playback time emits) and stays continuous across call
    // boundaries — when a call ends, the queue shrinks by that call's length at
    // the same instant the next call's remaining time takes its place.
    private computeDelay(): number {
        return this.computeQueueTime() + this.remainingCurrentCallSec();
    }

    // ensureCallDuration decodes a queued call's audio once to learn its length
    // in seconds, caching the result. Re-emits the queue state when the decode
    // lands so the LCD delay updates and auto-jump can re-evaluate.
    private ensureCallDuration(call: RdioScannerCall | undefined): void {
        if (!call?.id || !call.audio?.data?.length || this.callDurations.has(call.id)) {
            return;
        }

        const ctx = this.getDecodeContext();
        if (!ctx) {
            return;
        }

        // Reserve the slot up-front so a second decode isn't kicked off for the
        // same call before this one resolves.
        this.callDurations.set(call.id, 0);

        // Bound memory: when the cache gets large, drop everything not in the
        // current queue, then re-reserve this call's slot.
        if (this.callDurations.size > 512) {
            const keep = new Set(this.callQueue.map((c) => c.id));
            for (const id of Array.from(this.callDurations.keys())) {
                if (!keep.has(id)) {
                    this.callDurations.delete(id);
                }
            }
            this.callDurations.set(call.id, 0);
        }

        const id = call.id;
        const data = call.audio.data;
        const arrayBuffer = new ArrayBuffer(data.length);
        const arrayBufferView = new Uint8Array(arrayBuffer);

        for (let i = 0; i < data.length; i++) {
            arrayBufferView[i] = data[i];
        }

        ctx.decodeAudioData(arrayBuffer, (buffer) => {
            this.callDurations.set(id, buffer.duration || 0);
            this.maybeAutoJumpAhead();
            this.emitQueueChange();
        }, () => {
            // Decode failed — leave the placeholder 0. Unknown durations just
            // don't contribute to the displayed delay.
        });
    }

    private getDecodeContext(): BaseAudioContext | undefined {
        if (this.audioContext) {
            return this.audioContext;
        }

        if (!this.decodeContext) {
            try {
                this.decodeContext = new (window.AudioContext || window.webkitAudioContext)();
            } catch (e) {
                return undefined;
            }
        }

        return this.decodeContext;
    }

    // maybeAutoJumpAhead keeps the live-feed queue within a buffer: it drops the
    // oldest queued calls (front of the FIFO) until the combined queue delay is
    // back under the user-set threshold, so a listener who fell behind catches
    // up toward live without skipping straight to the newest call. Only acts
    // when the user toggle is on and we're in live (Online) mode; the
    // currently-playing call is left untouched.
    private maybeAutoJumpAhead(): void {
        if (!this.autoJumpAhead || this.livefeedMode !== RdioScannerLivefeedMode.Online) {
            return;
        }

        // Temporarily suspended while a hold (system or talkgroup) is active —
        // holding means the listener is deliberately staying on a conversation,
        // so don't jump them ahead. Resumes on the next call once the hold is
        // released. The LCD button shows yellow while in this state.
        if (this.isHoldActive()) {
            return;
        }

        const threshold = this.autoJumpThresholdMin * 60;
        const before = this.computeQueueTime();
        let trimmed = false;

        while (this.callQueue.length > 0 && this.computeDelay() > threshold) {
            const dropped = this.callQueue.shift();
            if (dropped?.id) {
                this.callDurations.delete(dropped.id);
            }
            trimmed = true;
        }

        if (!trimmed) {
            return;
        }

        // How much delay we just shed — broadcast it so the LCD can flash a
        // "-m:ss" next to the (instantly updated) new delay.
        const removed = Math.max(0, before - this.computeQueueTime());

        // If we trimmed the backlog while nothing was actively playing — the
        // toggle was flipped between calls, or playback had stalled while the
        // queue piled up (which is the only way to get minutes behind on a
        // live feed in the first place) — restart playback so the kept calls
        // actually play instead of sitting silently in the queue.
        if (
            !this.call &&
            !this.audioSource &&
            !this.livefeedPaused &&
            !this.skipDelay &&
            this.callQueue.length > 0
        ) {
            this.play();
        }

        // Always Online here (guarded above), so the queue count is the live
        // callQueue length.
        this.event.emit({
            queue: this.callQueue.length,
            queueTime: this.computeDelay(),
            queueJumped: removed,
        });
    }

    // A system or talkgroup hold is active when its pre-hold livefeed map has
    // been stashed away (the same signal the holdSys/holdTg events derive from).
    private isHoldActive(): boolean {
        return !!this.livefeedMapPriorToHoldSystem || !!this.livefeedMapPriorToHoldTalkgroup;
    }

    private emitQueueChange(): void {
        this.event.emit({
            queue: this.livefeedMode === RdioScannerLivefeedMode.Playback
                ? this.getPlaybackQueueCount()
                : this.callQueue.length,
            queueTime: this.computeDelay(),
        });
    }

    // Maximum gain value to prevent audio from being too loud
    // Slider at 100% = 0.575 actual gain (~58% of max browser volume, ~15% louder than before)
    private static readonly MAX_GAIN = 0.575;

    private updateGainNode(): void {
        if (this.gainNode) {
            this.gainNode.gain.value = this.muted ? 0 : this.volume * RdioScannerService.MAX_GAIN;
        }
    }

    private loadVolumeSettings(): void {
        try {
            const stored = window?.localStorage?.getItem('rdio-scanner-volume');
            if (stored) {
                const settings = JSON.parse(stored);
                if (typeof settings.volume === 'number') {
                    this.volume = Math.max(0, Math.min(1, settings.volume));
                }
                if (typeof settings.muted === 'boolean') {
                    this.muted = settings.muted;
                }
            }
        } catch (e) {
            // Ignore parse errors
        }
    }

    private saveVolumeSettings(): void {
        try {
            window?.localStorage?.setItem('rdio-scanner-volume', JSON.stringify({
                volume: this.volume,
                muted: this.muted,
            }));
        } catch (e) {
            // Ignore storage errors
        }
    }

    // Preset management methods
    getPresets(): RdioScannerPreset[] {
        try {
            const stored = window?.localStorage?.getItem('rdio-scanner-presets');
            if (stored) {
                return JSON.parse(stored);
            }
        } catch (e) {
            // Ignore parse errors
        }
        return [];
    }

    savePreset(preset: RdioScannerPreset): void {
        const presets = this.getPresets();
        const index = presets.findIndex(p => p.id === preset.id);
        if (index >= 0) {
            presets[index] = preset;
        } else {
            presets.push(preset);
        }
        this.savePresets(presets);
    }

    deletePreset(presetId: string): void {
        const presets = this.getPresets().filter(p => p.id !== presetId);
        this.savePresets(presets);
    }

    applyPreset(preset: RdioScannerPreset, activate: boolean): void {
        if (!this.config?.systems) {
            return;
        }

        // Apply preset to all talkgroups
        preset.talkgroups.forEach(({ systemId, talkgroupId }) => {
            const system = this.config?.systems?.find(s => s.id === systemId);
            const talkgroup = system?.talkgroups?.find(tg => tg.id === talkgroupId);
            if (system && talkgroup) {
                // Ensure the map structure exists
                if (!this.livefeedMap[systemId]) {
                    this.livefeedMap[systemId] = {};
                }
                if (!this.livefeedMap[systemId][talkgroupId]) {
                    this.livefeedMap[systemId][talkgroupId] = {
                        active: false,
                        minutes: undefined,
                        timer: undefined,
                    };
                }

                const lfm = this.livefeedMap[systemId][talkgroupId];
                lfm.active = activate;
                if (lfm.timer) {
                    lfm.timer.unsubscribe();
                    lfm.timer = undefined;
                }
                lfm.minutes = undefined;
            }
        });

        this.rebuildCategories();
        this.saveLivefeedMap();
        this.cleanQueue();

        if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
            this.startLivefeed();
        }

        this.event.emit({
            categories: this.categories,
            map: this.livefeedMap,
            queue: this.callQueue.length,
            queueTime: this.computeDelay(),
        });
    }

    exportPresets(): string {
        const presets = this.getPresets();
        const exportData: RdioScannerPresetExport = {
            version: '1.0',
            presets: presets,
            exportedAt: Date.now(),
        };
        return JSON.stringify(exportData, null, 2);
    }

    importPresets(json: string): { success: boolean; error?: string; count?: number } {
        try {
            const data: RdioScannerPresetExport = JSON.parse(json);
            if (!data.presets || !Array.isArray(data.presets)) {
                return { success: false, error: 'Invalid preset format' };
            }
            this.savePresets(data.presets);
            return { success: true, count: data.presets.length };
        } catch (e) {
            return { success: false, error: e instanceof Error ? e.message : 'Parse error' };
        }
    }

    private savePresets(presets: RdioScannerPreset[]): void {
        try {
            window?.localStorage?.setItem('rdio-scanner-presets', JSON.stringify(presets));
        } catch (e) {
            // Ignore storage errors
        }
    }

    play(call?: RdioScannerCall | undefined): void {
        if (this.livefeedPaused || this.skipDelay) {
            return;

        } else if (call?.audio) {
            if (this.call) {
                this.stop({ emit: false });
            }

            this.call = call;

        } else if (this.call) {
            return;

        } else {
            this.call = this.callQueue.shift();
        }

        if (!this.call?.audio) {
            return;
        }

        // Emit the call metadata before we try to decode audio. Browsers
        // block AudioContext until a user gesture, so on a cold share-link
        // landing beginAudioPlayback() bails at the !audioContext guard and
        // the only downstream {call} emit (inside decodeAudioData) never
        // fires. Components that anchor on event.call (LCD display, search
        // deep-link highlight + date-filter) would otherwise sit waiting
        // forever for a gesture that never comes.
        const queue = this.livefeedMode === RdioScannerLivefeedMode.Playback
            ? this.getPlaybackQueueCount()
            : this.callQueue.length;
        this.event.emit({ call: this.call, queue, queueTime: this.computeDelay() });

        this.beginAudioPlayback();
    }

    // beginAudioPlayback decodes this.call.audio and starts the source node.
    // Separated from play() so the wait-for-transcript path can defer it.
    private beginAudioPlayback(): void {
        if (!this.call?.audio || !this.audioContext) {
            return;
        }

        const queue = this.livefeedMode === RdioScannerLivefeedMode.Playback
            ? this.getPlaybackQueueCount()
            : this.callQueue.length;

        const arrayBuffer = new ArrayBuffer(this.call.audio.data.length);
        const arrayBufferView = new Uint8Array(arrayBuffer);

        for (let i = 0; i < (this.call.audio.data.length); i++) {
            arrayBufferView[i] = this.call.audio.data[i];
        }

        const generation = ++this.playGeneration;

        this.audioContext.decodeAudioData(arrayBuffer, (buffer) => {
            if (generation !== this.playGeneration) {
                return;
            }

            if (!this.audioContext || this.audioSource || !this.call) {
                return;
            }

            this.audioSource = this.audioContext.createBufferSource();
            this.audioSource.buffer = buffer;

            // Cache the now-known length of the call we're about to play so the
            // delay readout can include its remaining time and tick it down.
            if (this.call?.id) {
                this.callDurations.set(this.call.id, buffer.duration || 0);
            }

            if (!this.gainNode) {
                this.gainNode = this.audioContext.createGain();
                this.gainNode.connect(this.audioContext.destination);
                this.updateGainNode();
            }

            this.audioSource.connect(this.gainNode);
            this.audioSource.onended = () => this.skip({ delay: true });

            const alertName = this.getCallAlertName(this.call);
            const alertDuration = alertName
                ? this.scheduleAlert(alertName, this.audioContext.currentTime)
                : 0;
            this.audioSource.start(this.audioContext.currentTime + alertDuration);

            this.event.emit({ call: this.call, queue, queueTime: this.computeDelay() });

            interval(500).pipe(takeWhile(() => !!this.call)).subscribe(() => {
                if (this.audioContext && !isNaN(this.audioContext.currentTime)) {
                    if (isNaN(this.audioSourceStartTime)) {
                        this.audioSourceStartTime = this.audioContext.currentTime;
                    }

                    if (!this.livefeedPaused) {
                        this.event.emit({
                            time: this.audioContext.currentTime - this.audioSourceStartTime,
                            queueTime: this.computeDelay(),
                        });
                    }
                }
            });
        }, () => {
            if (generation !== this.playGeneration) {
                return;
            }

            this.event.emit({ call: this.call, queue, queueTime: this.computeDelay() });

            this.skip({ delay: false });
        });
    }

    // holdPendingTranscript parks a call in the ordered pre-queue. Two cases:
    //
    //   - Call already has a transcript (e.g., upstream-forwarded with the
    //     transcript baked in): the entry goes in marked ready and no timers
    //     are set. Drain still gates release on head-of-line arrival order,
    //     so the ready entry only escapes once earlier entries are also ready.
    //
    //   - Call has no transcript yet: the entry goes in NOT ready, with a
    //     polling fetch timer and a final timeout. The transcript arrival
    //     (via fetchTranscript) or timeout flips the ready flag and then
    //     drainPendingHead releases consecutive head-of-line ready entries.
    //
    // Calls behind a slow head wait — preserving arrival order matches what
    // the listener expects.
    private holdPendingTranscript(call: RdioScannerCall, priority: boolean): void {
        if (!call.id) {
            // No id to reconcile against — fall back to normal queueing.
            this.enqueuePending(call, priority);
            return;
        }

        const id = call.id;
        // If we're already holding this id (e.g., duplicate CAL), ignore.
        if (this.pendingTranscriptCalls.some((e) => e.id === id)) {
            return;
        }

        if (call.transcript) {
            // Already-transcribed: no polling needed; mark ready and let the
            // drain handle order. If it's at the head (no earlier entries),
            // drainPendingHead will release it immediately.
            this.pendingTranscriptCalls.push({ id, call, priority, ready: true });
            this.drainPendingHead();
            return;
        }

        const fetchTimer = setInterval(() => {
            this.fetchTranscript(id).then((text) => {
                if (text) {
                    this.markPendingReady(id, text);
                }
            }).catch(() => { /* ignore */ });
        }, RdioScannerService.TRANSCRIPT_WAIT_POLL_MS);

        const timeoutTimer = setTimeout(() => {
            // Final safety net: release whatever we have so the call isn't lost.
            this.markPendingReady(id, undefined);
        }, RdioScannerService.TRANSCRIPT_WAIT_MAX_MS);

        this.pendingTranscriptCalls.push({ id, call, priority, ready: false, fetchTimer, timeoutTimer });
    }

    // markPendingReady flips a pending entry's `ready` flag (and stashes a
    // late-arriving transcript if any), then drains any consecutive
    // head-of-line ready entries to the main queue. Calls behind a still-
    // not-ready head stay parked.
    private markPendingReady(id: number, transcript: string | undefined): void {
        const entry = this.pendingTranscriptCalls.find((e) => e.id === id);
        if (!entry || entry.ready) return;

        clearInterval(entry.fetchTimer);
        clearTimeout(entry.timeoutTimer);
        if (transcript) {
            entry.call.transcript = transcript;
        }
        entry.ready = true;

        this.drainPendingHead();
    }

    // drainPendingHead pops consecutive ready entries off the head of
    // pendingTranscriptCalls and enqueues them. Stops at the first
    // not-ready entry so arrival order is preserved.
    private drainPendingHead(): void {
        while (this.pendingTranscriptCalls.length > 0 && this.pendingTranscriptCalls[0].ready) {
            const entry = this.pendingTranscriptCalls.shift()!;
            this.enqueuePending(entry.call, entry.priority);
        }
    }

    // applyLateTranscript splices a transcript text into any call object we
    // know about with the given id — the held pre-queue entry, the call
    // currently playing, and any call still sitting in the main playback
    // queue. Used when a transcript arrives via WS push after a held call's
    // 15s wait-for-transcript timeout already released it without text.
    //
    // Returns true if at least one in-memory call was updated, so callers
    // can decide whether further processing is needed.
    private applyLateTranscript(id: number, text: string): boolean {
        let touched = false;

        // Held in the pre-queue (still ready=false or already-ready). Update
        // the entry's call payload so when it's released the user sees the
        // transcript. Doesn't change ordering.
        for (const entry of this.pendingTranscriptCalls) {
            if (entry.id === id && !entry.call.transcript) {
                entry.call.transcript = text;
                touched = true;
            }
        }

        // Currently playing call.
        if (this.call && this.call.id === id && !this.call.transcript) {
            this.call.transcript = text;
            // Re-emit so the LCD / live-feed panel re-renders with the text.
            this.event.emit({ call: this.call });
            touched = true;
        }

        // Anything still queued.
        for (const queued of this.callQueue) {
            if (queued.id === id && !queued.transcript) {
                queued.transcript = text;
                touched = true;
            }
        }

        return touched;
    }

    // enqueuePending is the normal queue path that a held call takes once
    // its transcript has arrived (or its timeout elapsed).
    private enqueuePending(call: RdioScannerCall, priority: boolean): void {
        if (this.livefeedMode === RdioScannerLivefeedMode.Offline) {
            // Live feed was turned off while we were holding — silently drop.
            return;
        }
        // Re-check the livefeedMap: the talkgroup may have been avoided or
        // a category toggled off while the transcript was pending. The
        // livefeedMode check alone misses these cases.
        if (this.livefeedMap[call.system]?.[call.talkgroup]?.active === false) {
            console.debug('enqueuePending: dropping held call; talkgroup no longer active',
                { id: call.id, system: call.system, talkgroup: call.talkgroup });
            return;
        }
        if (priority) {
            this.callQueue.unshift(call);
        } else {
            this.callQueue.push(call);
        }
        this.ensureCallDuration(call);
        this.maybeAutoJumpAhead();
        if (this.audioSource || this.call || this.livefeedPaused || this.skipDelay) {
            this.event.emit({
                queue: this.livefeedMode === RdioScannerLivefeedMode.Online ? this.callQueue.length : this.getPlaybackQueueCount(),
                queueTime: this.computeDelay(),
            });
        } else {
            this.play();
        }
    }

    // flushPendingTranscripts dumps all held calls into the queue in their
    // existing arrival order. Used when the admin turns the toggle off
    // mid-session.
    private flushPendingTranscripts(): void {
        const entries = this.pendingTranscriptCalls.splice(0);
        for (const entry of entries) {
            clearInterval(entry.fetchTimer);
            clearTimeout(entry.timeoutTimer);
            this.enqueuePending(entry.call, entry.priority);
        }
    }

    queue(call: RdioScannerCall, options?: { priority?: boolean }): void {
        if (!call?.audio || this.livefeedMode === RdioScannerLivefeedMode.Offline) {
            return;
        }

        // When wait-for-transcript is on, route ALL non-priority calls
        // through the pre-queue — even ones that already arrived with a
        // transcript attached (e.g., upstream-forwarded calls where the
        // server applied a deferred transcript from cache before emitting).
        // Otherwise pre-transcribed calls would skip the pre-queue and play
        // ahead of earlier calls still waiting on local Whisper, breaking
        // arrival-order playback.
        if (this.waitForTranscript && !options?.priority) {
            this.holdPendingTranscript(call, false);
            return;
        }

        if (options?.priority) {
            this.callQueue.unshift(call);

        } else {
            this.callQueue.push(call);
        }

        this.ensureCallDuration(call);
        this.maybeAutoJumpAhead();

        if (this.audioSource || this.call || this.livefeedPaused || this.skipDelay) {
            this.event.emit({
                queue: this.livefeedMode === RdioScannerLivefeedMode.Online ? this.callQueue.length : this.getPlaybackQueueCount(),
                queueTime: this.computeDelay(),
            });

        } else {
            this.play();
        }
    }

    replay(): void {
        this.play(this.call || this.callPrevious);
    }

    readPin(): string | undefined {
        const pin = window?.localStorage?.getItem(RdioScannerService.LOCAL_STORAGE_KEY_PIN);

        return pin ? window.atob(pin) : undefined;
    }

    savePin(pin: string): void {
        window?.localStorage?.setItem(RdioScannerService.LOCAL_STORAGE_KEY_PIN, window.btoa(pin));
    }

    searchCalls(options: RdioScannerSearchOptions): void {
        this.trackUmamiEvent('call-search');
        this.sendtoWebsocket(WebsocketCommand.ListCall, options);
    }

    fetchTranscript(id: number): Promise<string> {
        return new Promise<string>((resolve) => {
            if (!id) {
                resolve('');
                return;
            }
            const existing = this.transcriptResolvers.get(id);
            if (existing) {
                existing('');
            }
            this.transcriptResolvers.set(id, (text) => {
                this.transcriptResolvers.delete(id);
                resolve(text);
            });
            this.sendtoWebsocket(WebsocketCommand.Transcript, id);

            setTimeout(() => {
                const r = this.transcriptResolvers.get(id);
                if (r) {
                    this.transcriptResolvers.delete(id);
                    r('');
                }
            }, 10000);
        });
    }

    skip(options?: { delay?: boolean }): void {
        const play = () => {
            if (this.livefeedMode === RdioScannerLivefeedMode.Playback) {
                this.playbackNextCall();

            } else {
                this.play();
            }
        };

        this.stop();

        if (options?.delay) {
            this.skipDelay = timer(1000).subscribe(() => {
                this.skipDelay = undefined;

                play();
            });

        } else {
            if (this.skipDelay) {
                this.skipDelay?.unsubscribe();

                this.skipDelay = undefined;
            }

            play();
        }
    }

    startLivefeed(): void {
        const lfm = Object.keys(this.livefeedMap).reduce((sysMap: { [key: number]: { [key: number]: boolean } }, sys) => {
            sysMap[+sys] = Object.keys(this.livefeedMap[+sys]).reduce((tgMap: { [key: number]: boolean }, tg: string) => {
                tgMap[+tg] = this.livefeedMap[+sys][+tg].active;
                return tgMap;
            }, {});
            return sysMap;
        }, {});

        this.livefeedMode = RdioScannerLivefeedMode.Online;

        this.trackUmamiEvent('livefeed-start');

        this.event.emit({ livefeedMode: this.livefeedMode });

        this.sendtoWebsocket(WebsocketCommand.LivefeedMap, lfm);

        // Mirror the freshly-subscribed selection to any /stream followers.
        this.broadcastLivefeedState();
    }

    stop(options?: { emit?: boolean }): void {
        // Invalidate any in-flight decodeAudioData callbacks. Without this,
        // a stale decode that lands after stop() would still create a source
        // for the old buffer and start playing it.
        this.playGeneration++;

        if (this.audioSource) {
            this.audioSource.onended = null;
            this.audioSource.stop();
            this.audioSource.disconnect();
            this.audioSource = undefined;
            this.audioSourceStartTime = NaN;
        }

        if (this.call) {
            this.callPrevious = this.call;

            this.call = undefined;
        }

        if (typeof options?.emit !== 'boolean' || options.emit) {
            this.event.emit({ call: this.call });
        }
    }

    stopLivefeed(): void {
        this.trackUmamiEvent('livefeed-stop');

        this.livefeedMode = RdioScannerLivefeedMode.Offline;

        this.clearQueue();

        this.event.emit({ livefeedMode: this.livefeedMode, queue: 0 });

        this.stop();

        this.sendtoWebsocket(WebsocketCommand.LivefeedMap, null);
    }

    stopPlaybackMode(): void {
        this.livefeedMode = RdioScannerLivefeedMode.Offline;

        this.playbackRefreshing = false;

        this.clearQueue();

        this.event.emit({ livefeedMode: this.livefeedMode, queue: 0 });

        this.stop();
    }

    toggleCategory(category: RdioScannerCategory): void {
        const clearTimer = (lfm: RdioScannerLivefeed): void => {
            lfm.minutes = 0;
            lfm.timer?.unsubscribe();
            lfm.timer = undefined;
        };

        if (category) {
            if (this.livefeedMapPriorToHoldSystem) {
                this.livefeedMapPriorToHoldSystem = undefined;
            }

            if (this.livefeedMapPriorToHoldTalkgroup) {
                this.livefeedMapPriorToHoldTalkgroup = undefined;
            }

            const status = category.status === RdioScannerCategoryStatus.On ? false : true;

            this.config?.systems.forEach((sys) => {
                sys.talkgroups?.forEach((tg) => {
                    const lfm = this.livefeedMap[sys.id][tg.id];

                    if (category.type == RdioScannerCategoryType.Group && tg.group === category.label) {
                        clearTimer(lfm);
                        lfm.active = status;
                    } else if (category.type == RdioScannerCategoryType.Tag && tg.tag === category.label) {
                        clearTimer(lfm);
                        lfm.active = status;
                    }
                });
            });

            this.rebuildCategories();

            if (this.call && !this.livefeedMap[this.call.system] && this.livefeedMap[this.call.system][this.call.talkgroup]) {
                clearTimer(this.livefeedMap[this.call.system][this.call.talkgroup]);
                this.skip();
            }

            if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
                this.startLivefeed();
            }

            this.saveLivefeedMap();

            this.cleanQueue();

            this.event.emit({
                categories: this.categories,
                holdSys: false,
                holdTg: false,
                map: this.livefeedMap,
                queue: this.callQueue.length,
                queueTime: this.computeDelay(),
            });
        }
    }

    private bootstrapAudio(): void {
        const events = ['keydown', 'mousedown', 'touchstart'];

        const bootstrap = async () => {
            if (!this.audioContext) {
                this.audioContext = new (window.AudioContext || window.webkitAudioContext)({ latencyHint: 'playback' });
            }

            if (!this.beepContext) {
                this.beepContext = new (window.AudioContext || window.webkitAudioContext)({ latencyHint: 'interactive' });
            }

            if (this.audioContext && !this.gainNode) {
                this.gainNode = this.audioContext.createGain();
                this.gainNode.connect(this.audioContext.destination);
                this.updateGainNode();
            }

            if (this.audioContext) {
                const resume = () => {
                    if (!this.livefeedPaused) {
                        if (this.audioContext?.state === 'suspended') {
                            this.audioContext?.resume().then(() => resume());
                        }
                    }
                };

                await this.audioContext.resume();

                this.audioContext.onstatechange = () => resume();
            }

            if (this.beepContext) {
                const resume = () => {
                    if (this.beepContext?.state === 'suspended') {
                        this.beepContext?.resume().then(() => resume());
                    }
                };

                await this.beepContext.resume();

                this.beepContext.onstatechange = () => resume();
            }

            if (this.audioContext && this.beepContext) {
                events.forEach((event) => document.body.removeEventListener(event, bootstrap));
            }

            // Share-link / deep-link landings set this.call via play() before
            // audioContext exists, so beginAudioPlayback() bailed at the
            // !audioContext guard. Now that the user has gestured and we
            // have a live context, drive the deferred playback.
            if (this.call?.audio && !this.audioSource) {
                this.beginAudioPlayback();
            }
        };

        events.forEach((event) => document.body.addEventListener(event, bootstrap));
    }

    private cleanQueue(): void {
        const isActive = (call: RdioScannerCall) => {
            const lfm = (sys: number, tg: number): boolean => this.livefeedMap && this.livefeedMap[sys] && this.livefeedMap[sys][tg]?.active;
            let active = lfm(call.system, call.talkgroup);
            if (!active && Array.isArray(call.patches)) {
                for (let i = 0; i < call.patches.length; i++) {
                    active = lfm(call.system, call.patches[i]);
                    if (active) {
                        break;
                    }
                }
            }
            return active;
        };

        this.callQueue = this.callQueue.filter((call: RdioScannerCall) => isActive(call));

        // Also drop any pending-transcript holds whose talkgroup is no
        // longer active. Their timers would otherwise keep polling for up
        // to 30s and then enqueue the call into a queue that no longer
        // wants it. Filtering in-place preserves arrival order for the
        // entries we keep.
        this.pendingTranscriptCalls = this.pendingTranscriptCalls.filter((entry) => {
            if (isActive(entry.call)) return true;
            clearInterval(entry.fetchTimer);
            clearTimeout(entry.timeoutTimer);
            return false;
        });
        // A removed head could unblock newly-ready entries that were
        // waiting on it.
        this.drainPendingHead();

        if (this.call && !isActive(this.call)) {
            this.skip();
        }
    }

    private clearQueue(): void {
        this.callQueue.splice(0, this.callQueue.length);
    }

    private closeWebsocket(): void {
        if (this.websocket instanceof WebSocket) {
            this.websocket.onclose = null;
            this.websocket.onerror = null;
            this.websocket.onmessage = null;
            this.websocket.onopen = null;

            this.websocket.close();

            this.websocket = undefined;
        }
    }

    private download(call: RdioScannerCall): void {
        this.trackUmamiEvent('call-download', { callId: call.id });
        if (call.audio) {
            const file = call.audio.data.reduce((str, val) => str += String.fromCharCode(val), '');
            const fileName = call.audioName || 'unknown.dat';
            const fileType = call.audioType || 'audio/*';
            const fileUri = `data:${fileType};base64,${window.btoa(file)}`;

            const el = this.document.createElement('a');

            el.style.display = 'none';

            el.setAttribute('href', fileUri);
            el.setAttribute('download', fileName);

            this.document.body.appendChild(el);

            el.click();

            this.document.body.removeChild(el);
        }
    }

    private getCall(id: number, flags?: WebsocketCallFlag): void {
        // Diagnostic for share-link debugging. Pair this with the matching
        // [rdio] CAL response log to spot dropped requests (no response →
        // server-side silent drop, see ProcessMessageCommandCall).
        console.log(`[rdio] CAL request id=${id} flag=${flags}`);
        this.sendtoWebsocket(WebsocketCommand.Call, `${id}`, flags);
    }

    private getPlaybackQueueCount(id = this.call?.id || this.callPrevious?.id): number {
        let queueCount = 0;

        if (id && this.playbackList) {
            const index = this.playbackList.results.findIndex((call) => call.id === id);

            if (index !== -1) {
                if (this.playbackList.options.sort === -1) {
                    queueCount = this.playbackList.options.offset + index;

                } else {
                    queueCount = this.playbackList.count - this.playbackList.options.offset - index - 1;
                }
            }
        }

        return queueCount;
    }

    private initializeInstanceId(): void {
        this.instanceId = this.router.parseUrl(this.router.url).queryParams['id'] || this.instanceId;
    }

    private openWebsocket(): void {
        // Prefer the pre-opened socket from index.html if it's still usable.
        // That script fires before the Angular bundle downloads, so by the
        // time this runs the TLS + HTTP upgrade is typically already done —
        // no second round-trip to link.
        type EarlyWs = WebSocket & {
            __queue?: string[];
            __closed?: boolean;
            __errored?: boolean;
            __handler?: (data: string) => void;
        };
        const win = window as unknown as { __rdioEarlyWs?: EarlyWs };
        const early = win.__rdioEarlyWs;
        let queued: string[] | undefined;
        let ws: WebSocket;
        let early2: EarlyWs | undefined;

        if (early && !early.__closed && !early.__errored && early.readyState !== WebSocket.CLOSED) {
            ws = early;
            early2 = early;
            queued = early.__queue;
            win.__rdioEarlyWs = undefined;

        } else {
            const websocketUrl = window.location.href.replace(/^http/, 'ws');
            ws = new WebSocket(websocketUrl);
        }

        this.websocket = ws;

        ws.onclose = (ev: CloseEvent) => {
            this.linkedState = false;
            this.event.emit({ linked: false });

            if (ev.code !== 1000) {
                timer(2000).subscribe(() => this.reconnectWebsocket());
            }
        };

        const parse = (data: string) => this.parseWebsocketMessage(data);

        const onOpen = () => {
            this.linkedState = true;
            this.event.emit({ linked: true });

            if (early2) {
                // Swap the early-buffer handler to the real parser, then
                // drop the buffer so subsequent messages go straight through.
                early2.__handler = parse;
                early2.__queue = undefined;
            } else {
                ws.onmessage = (ev: MessageEvent) => parse(ev.data);
            }

            this.sendtoWebsocket(WebsocketCommand.Version);
            this.sendtoWebsocket(WebsocketCommand.Config);

            this.flushPendingSends();

            if (queued && queued.length) {
                for (const data of queued) {
                    parse(data);
                }
            }
            queued = undefined;
        };

        if (ws.readyState === WebSocket.OPEN) {
            onOpen();
        } else {
            ws.onopen = onOpen;
        }
    }

    private parseWebsocketMessage(message: string): void {
        try {
            message = JSON.parse(message);

        } catch (error) {
            console.warn(`Invalid control message received, ${error}`);
        }

        if (Array.isArray(message)) {
            switch (message[0]) {
                case WebsocketCommand.Call:
                    if (message[1] === null) {
                        console.warn('[rdio] CAL response: payload was null (server rejected or dropped the request)');
                    } else if (message[1] !== null) {
                        const call: RdioScannerCall = message[1];
                        const flag: string = message[2];
                        const hasAudio = !!(call.audio && (call.audio as { data?: unknown }).data);
                        console.log(`[rdio] CAL response id=${call.id} flag=${flag} hasAudio=${hasAudio} system=${call.system} talkgroup=${call.talkgroup} playbackPending=${this.playbackPending}`);
                        if (!hasAudio) {
                            console.warn(`[rdio] CAL response id=${call.id} arrived without audio — share-link playback will silently bail. Likely cause: server returned a zombie empty Call (pre-fix) or call was purged.`);
                        }

                        if (flag === WebsocketCallFlag.Download) {
                            this.download(message[1]);

                        } else if (flag === WebsocketCallFlag.Play && call.id === this.playbackPending) {
                            this.playbackPending = undefined;

                            this.queue(this.transformCall(call), { priority: true });

                        } else {
                            this.queue(this.transformCall(call));
                        }
                    }

                    break;

                case WebsocketCommand.Config: {
                    const config = message[1];

                    this.config = {
                        alerts: config.alerts !== null && typeof config.alerts === 'object' ? config.alerts : undefined,
                        branding: typeof config.branding === 'string' ? config.branding : '',
                        dimmerDelay: typeof config.dimmerDelay === 'number' ? config.dimmerDelay : 5000,
                        email: typeof config.email === 'string' ? config.email : '',
                        groups: typeof config.groups !== null && typeof config.groups === 'object' ? config.groups : {},
                        keypadBeeps: config.keypadBeeps !== null && typeof config.keypadBeeps === 'object' ? config.keypadBeeps : {},
                        playbackGoesLive: typeof config.playbackGoesLive === 'boolean' ? config.playbackGoesLive : false,
                        showListenersCount: typeof config.showListenersCount === 'boolean' ? config.showListenersCount : false,
                        systems: Array.isArray(config.systems) ? config.systems.slice() : [],
                        tags: typeof config.tags !== null && typeof config.tags === 'object' ? config.tags : {},
                        tagsToggle: typeof config.tagsToggle === 'boolean' ? config.tagsToggle : false,
                        time12hFormat: typeof config.time12hFormat === 'boolean' ? config.time12hFormat : false,
                        umamiUrl: typeof config.umamiUrl === 'string' ? config.umamiUrl : undefined,
                        umamiWebsiteId: typeof config.umamiWebsiteId === 'string' ? config.umamiWebsiteId : undefined,
                        showRetranscribeButton: typeof config.showRetranscribeButton === 'boolean' ? config.showRetranscribeButton : false,
                    };

                    // Server-driven wait-for-transcript (admin option).
                    const nextWait = typeof config.waitForTranscript === 'boolean' ? config.waitForTranscript : false;
                    if (nextWait !== this.waitForTranscript) {
                        this.waitForTranscript = nextWait;
                        this.event.emit({ waitForTranscript: nextWait });
                        if (!nextWait) {
                            this.flushPendingTranscripts();
                        }
                    }

                    this.setupUmami();

                    if (typeof config.afs === 'string' && config.afs.length) {
                        this.config['afs'] = config.afs;
                    }

                    this.rebuildLivefeedMap();

                    if (this.livefeedMode === RdioScannerLivefeedMode.Online) {
                        this.startLivefeed();
                    }

                    // Snapshot state for late subscribers before emitting.
                    this.configState = this.config;
                    this.categoriesState = this.categories;
                    this.livefeedMapState = this.livefeedMap;

                    this.event.emit({
                        auth: false,
                        categories: this.categories,
                        config: this.config,
                        holdSys: !!this.livefeedMapPriorToHoldSystem,
                        holdTg: !!this.livefeedMapPriorToHoldTalkgroup,
                        map: this.livefeedMap,
                    });

                    if (this.pendingDeepLinkCallId) {
                        // Hand off to the search component (via the top-level
                        // rdio-scanner component) so it can open the search
                        // panel, locate the call in the results, highlight
                        // its row, and start playback.
                        //
                        // Don't clear pendingDeepLinkCallId here — the
                        // subscriber consumes it via consumePendingDeepLink()
                        // which also handles the late-subscribe race where
                        // this emit lands before the component subscribes.
                        const id = this.pendingDeepLinkCallId;
                        setTimeout(() => this.event.emit({ deepLinkCall: id }), 250);
                    }

                    break;
                }

                case WebsocketCommand.Expired:
                    this.event.emit({ auth: true, expired: true });

                    break;

                case WebsocketCommand.ListCall:
                    this.playbackList = message[1];

                    if (this.playbackList) {
                        this.playbackList.results = this.playbackList.results.map((call) => this.transformCall(call));

                        this.event.emit({ playbackList: this.playbackList });

                        if (this.livefeedMode === RdioScannerLivefeedMode.Playback) {
                            this.playbackNextCall();
                        }
                    }

                    break;

                case WebsocketCommand.ListenersCount:
                    this.event.emit({ listeners: message[1] });

                    break;

                case WebsocketCommand.Max:
                    this.event.emit({ auth: true, tooMany: true });

                    break;

                case WebsocketCommand.Pin:
                    this.event.emit({ auth: true });

                    break;

                case WebsocketCommand.Transcript: {
                    const payload = message[1];
                    if (payload && typeof payload === 'object') {
                        const id = (payload as any).id;
                        const text: string = typeof (payload as any).transcript === 'string' ? (payload as any).transcript : '';
                        if (typeof id === 'number' && text) {
                            // Apply the transcript in-place to any held / queued /
                            // currently-playing call with this id. Handles the
                            // "wait-for-transcript timed out at 15s, then the
                            // transcript finally arrived 20s later" case so the
                            // call's transcript still updates instead of being
                            // silently lost.
                            this.applyLateTranscript(id, text);

                            const resolver = this.transcriptResolvers.get(id);
                            if (resolver) {
                                // Pending fetchTranscript() request — resolve it.
                                resolver(text);
                            } else {
                                // Unsolicited push from server (a transcription
                                // completed for a call we already know about).
                                // Let components that show history splice it in.
                                this.event.emit({ transcriptReady: { id, transcript: text } });
                            }
                        }
                    }
                    break;
                }

                case WebsocketCommand.Version: {
                    const data = message[1];

                    if (data !== null && typeof data === 'object') {
                        const branding = data['branding'];
                        const email = data['email'];

                        if (typeof branding === 'string') {
                            this.config.branding = branding;
                        }

                        if (typeof email === 'string') {
                            this.config.email = email;
                        }

                        if (this.config.branding || this.config.email) {
                            this.configState = this.config;
                            this.event.emit({ config: this.config });
                        }
                    }

                    break;
                }
            }
        }
    }

    private playbackNextCall(): void {
        if (this.call || this.livefeedMode !== RdioScannerLivefeedMode.Playback || !this.playbackList || this.playbackPending) {
            return;
        }

        const index = this.playbackList.results.findIndex((call) => call.id === this.callPrevious?.id);

        if (this.playbackList.options.sort === -1) {
            if (index === -1) {
                this.loadAndPlay(this.playbackList.results[this.playbackList.results.length - 1].id);

            } else if (index === 0) {
                if (this.playbackList.options.offset < this.playbackList.options.limit) {
                    if (this.playbackRefreshing) {
                        this.stopPlaybackMode();

                        if (this.config.playbackGoesLive) {
                            this.startLivefeed();
                        }

                    } else {
                        this.playbackRefreshing = true;
                        this.searchCalls(this.playbackList.options);
                    }

                } else {
                    this.searchCalls(Object.assign({}, this.playbackList.options, {
                        offset: this.playbackList.options.offset - this.playbackList.options.limit,
                    }));
                }

            } else {
                this.loadAndPlay(this.playbackList.results[index - 1].id);
            }

        } else {
            if (index === -1) {
                this.loadAndPlay(this.playbackList.results[0].id);

            } else if (index === this.playbackList.results.length - 1) {
                if (this.playbackList.options.offset < (this.playbackList.count - this.playbackList.options.limit)) {
                    this.searchCalls(Object.assign({}, this.playbackList.options, {
                        offset: this.playbackList.options.offset + this.playbackList.options.limit,
                    }));

                } else if (this.playbackRefreshing) {
                    this.stopPlaybackMode();

                    if (this.config.playbackGoesLive) {
                        this.startLivefeed();
                    }

                } else {
                    this.playbackRefreshing = true;
                    this.searchCalls(this.playbackList.options);
                }

            } else {
                this.loadAndPlay(this.playbackList.results[index + 1].id);
            }
        }
    }

    private readLivefeedMap(): void {
        try {
            let lfm: { [key: number]: { [key: number]: boolean } } = {};

            let store = window?.localStorage?.getItem(`${RdioScannerService.LOCAL_STORAGE_KEY_LFM}-${this.instanceId}`);

            if (store !== null) {
                lfm = JSON.parse(store);

            } else {
                store = window?.localStorage?.getItem(RdioScannerService.LOCAL_STORAGE_KEY_LEGACY);

                if (store !== null) {
                    lfm = JSON.parse(store);
                }
            }

            Object.keys(lfm ?? {}).forEach((sys: string) => {
                Object.keys(lfm[+sys]).forEach((tg) => {
                    if (!this.livefeedMap[+sys]) this.livefeedMap[+sys] = {};
                    if (!this.livefeedMap[+sys][+tg]) this.livefeedMap[+sys][+tg] = {} as RdioScannerLivefeed;
                    this.livefeedMap[+sys][+tg].active = lfm[+sys][+tg];
                });
            });

        } catch (_) {
            //
        }
    }

    private rebuildCategories(): void {
        this.categories = Object.keys(this.config.groups || []).map((label) => {
            const allOff = Object.keys(this.config.groups[label]).map((sys) => +sys)
                .every((sys: number) => this.config.groups[label] && this.config.groups[label][sys]
                    .every((tg) => this.livefeedMap[sys] && !this.livefeedMap[sys][tg].active));

            const allOn = Object.keys(this.config.groups[label]).map((sys) => +sys)
                .every((sys: number) => this.config.groups[label] && this.config.groups[label][sys]
                    .every((tg) => this.livefeedMap[sys] && this.livefeedMap[sys][tg].active));

            const status = allOff ? RdioScannerCategoryStatus.Off : allOn ? RdioScannerCategoryStatus.On : RdioScannerCategoryStatus.Partial;

            return { label, status, type: RdioScannerCategoryType.Group };
        })

        if (this.config.tagsToggle) {
            this.categories = this.categories.concat(Object.keys(this.config.tags || []).map((label) => {
                const allOff = Object.keys(this.config.tags[label]).map((sys) => +sys)
                    .every((sys: number) => this.config.tags[label] && this.config.tags[label][sys]
                        .every((tg) => this.livefeedMap[sys] && !this.livefeedMap[sys][tg].active));

                const allOn = Object.keys(this.config.tags[label]).map((sys) => +sys)
                    .every((sys: number) => this.config.tags[label] && this.config.tags[label][sys]
                        .every((tg) => this.livefeedMap[sys] && this.livefeedMap[sys][tg].active));

                const status = allOff ? RdioScannerCategoryStatus.Off : allOn ? RdioScannerCategoryStatus.On : RdioScannerCategoryStatus.Partial;

                return { label, status, type: RdioScannerCategoryType.Tag };
            }))
        }

        this.categories.sort((a, b) => a.label.localeCompare(b.label));

        // Keep the late-subscriber snapshot in sync.
        this.categoriesState = this.categories;
    }

    private rebuildLivefeedMap(): void {
        const lfm = this.config.systems.reduce((sysMap, sys) => {
            sysMap[sys.id] = sys.talkgroups.reduce((tgMap, tg) => {
                const group = this.categories.find((cat) => cat.label === tg.group);
                const tag = this.categories.find((cat) => cat.label === tg.tag);

                tgMap[tg.id] = (this.livefeedMap[sys.id] && this.livefeedMap[sys.id][tg.id])
                    ? this.livefeedMap[sys.id][tg.id]
                    : {
                        active: !(group?.status === RdioScannerCategoryStatus.Off || tag?.status === RdioScannerCategoryStatus.Off),
                    } as RdioScannerLivefeed;

                return tgMap;
            }, sysMap[sys.id] || {} as { [key: number]: RdioScannerLivefeed });
            return sysMap;
        }, {} as RdioScannerLivefeedMap);

        if (this.livefeedMapPriorToHoldSystem != null) {
            this.livefeedMapPriorToHoldSystem = lfm;
        } else if (this.livefeedMapPriorToHoldTalkgroup != null) {
            this.livefeedMapPriorToHoldTalkgroup = lfm;
        } else {
            this.livefeedMap = lfm;
        }

        // Keep the late-subscriber snapshot in sync. Mirrors the active
        // livefeedMap regardless of which "prior to hold" branch was taken
        // so that getLivefeedMap() always returns the currently emitted map.
        this.livefeedMapState = this.livefeedMap;

        this.saveLivefeedMap();

        this.rebuildCategories();
    }

    private reconnectWebsocket(): void {
        this.closeWebsocket();

        this.openWebsocket();
    }

    private saveLivefeedMap(): void {
        const lfm = Object.keys(this.livefeedMap).reduce((sysMap: { [key: number]: { [key: number]: boolean } }, sys: string) => {
            sysMap[+sys] = Object.keys(this.livefeedMap[+sys]).reduce((tgMap: { [key: number]: boolean }, tg: string) => {
                tgMap[+tg] = this.livefeedMap[+sys][+tg].active;
                return tgMap;
            }, {});
            return sysMap;
        }, {});

        window?.localStorage?.setItem(`${RdioScannerService.LOCAL_STORAGE_KEY_LFM}-${this.instanceId}`, JSON.stringify(lfm));

        // Covers selection/avoid changes made while offline (no resubscribe,
        // so startLivefeed's broadcast wouldn't fire). No-op on followers.
        this.broadcastLivefeedState();
    }

    private sendtoWebsocket(command: string, payload?: unknown, flags?: string): void {
        if (this.websocket?.readyState === 1) {
            const message: unknown[] = [command];

            if (payload) {
                message.push(payload);
            }

            if (flags !== null && flags !== undefined) {
                message.push(flags);
            }

            this.websocket.send(JSON.stringify(message));
        } else {
            // Buffer commands issued before the socket is open (e.g. user
            // clicking LIVE FEED during the first-connect window). Flushed
            // from the onopen handler so the click isn't lost.
            this.pendingSends.push({ command, payload, flags });
        }
    }

    private pendingSends: { command: string; payload?: unknown; flags?: string }[] = [];

    private flushPendingSends(): void {
        if (!this.pendingSends.length) return;
        const queued = this.pendingSends.splice(0);
        for (const m of queued) {
            this.sendtoWebsocket(m.command, m.payload, m.flags);
        }
    }


    private setupUmami(): void {
        const doc = this.document;
        const existingScript = doc.getElementById('umami-script');

        if (this.config.umamiUrl && this.config.umamiWebsiteId) {
            if (existingScript) {
                if (existingScript.getAttribute('src') === this.config.umamiUrl
                    && existingScript.getAttribute('data-website-id') === this.config.umamiWebsiteId) {
                    return;
                }
                existingScript.remove();
            }

            const script = doc.createElement('script');
            script.id = 'umami-script';
            script.async = true;
            script.defer = true;
            script.setAttribute('src', this.config.umamiUrl);
            script.setAttribute('data-website-id', this.config.umamiWebsiteId);
            doc.head.appendChild(script);
        } else if (existingScript) {
            existingScript.remove();
        }
    }

    trackUmamiEvent(eventName: string, eventData?: Record<string, string | number>): void {
        try {
            const umami = (window as any).umami;
            if (umami) {
                umami.track(eventName, eventData);
            }
        } catch (_) {
            //
        }
    }

    private transformCall(call: RdioScannerCall): RdioScannerCall {
        if (call && Array.isArray(this.config?.systems)) {
            call.systemData = this.config.systems.find((system) => system.id === call.system);

            if (Array.isArray(call.systemData?.talkgroups)) {
                call.talkgroupData = call.systemData?.talkgroups.find((talkgroup) => talkgroup.id === call.talkgroup);
            }

            if (call.talkgroupData?.frequency) {
                call.frequency = call.talkgroupData.frequency;
            }
        }

        return call;
    }
}

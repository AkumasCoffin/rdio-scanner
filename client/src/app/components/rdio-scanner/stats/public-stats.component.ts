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

import { HttpClient } from '@angular/common/http';
import { Component, OnInit } from '@angular/core';
import { ChartConfiguration, ChartData, ChartType } from 'chart.js';
import { firstValueFrom } from 'rxjs';
import {
    STATS_RANGES, StatsChartCosmetics, StatsRange,
    buildCallsSeries, buildListenersSeries, buildTopSeries,
    callsDatasets, listenersDatasets, topDatasets,
    timeSeriesOptions, topSeriesOptions,
} from './stats-charts';

const COSMETICS: StatsChartCosmetics = { maxTicksLimit: 8, truncateLabels: 22, legendBoxWidth: 12 };

interface StatsOverview {
    totalCalls: number;
    activeSystems: number;
    activeTalkgroups: number;
}

interface StatsHourBucket {
    startUtc: string;
    count: number;
}

interface StatsTopTalkgroup {
    systemId: number;
    systemLabel: string;
    talkgroupId: number;
    talkgroupLabel: string;
    talkgroupName: string;
    count: number;
}

interface StatsTopSystem {
    systemId: number;
    systemLabel: string;
    count: number;
}

interface StatsTopUnit {
    systemId: number;
    systemLabel: string;
    unitId: number;
    unitLabel: string;
    count: number;
}

interface StatsLastHourTalkgroup {
    systemId: number;
    systemLabel: string;
    talkgroupId: number;
    talkgroupLabel: string;
    talkgroupName: string;
    count: number;
    lastCall: string;
}

interface StatsTalkgroupUnit {
    unitId: number;
    unitLabel: string;
    count: number;
    lastCall: string;
}

interface StatsListenerBucket {
    startUtc: string;
    avg: number;
    peak: number;
}

interface StatsTopCategory {
    label: string;
    count: number;
}

interface StatsResponse {
    overview: StatsOverview;
    hourBuckets: StatsHourBucket[];
    // Dense 10-minute call counts for the last 48 hours (short ranges).
    callFineBuckets?: StatsHourBucket[];
    topTalkgroups: StatsTopTalkgroup[];
    topSystems: StatsTopSystem[];
    // Option-aware "Top ..." ranking: groups / tags / systems.
    topCategories?: StatsTopCategory[];
    topCategoriesKind?: string;
    topUnits: StatsTopUnit[];
    lastHourTalkgroups: StatsLastHourTalkgroup[];
    // Absent unless the administrator enabled Show Listener Statistics —
    // the server strips the field, so its presence is the feature gate.
    listenerBuckets?: StatsListenerBucket[];
}

@Component({
    selector: 'rdio-scanner-public-stats',
    templateUrl: './public-stats.component.html',
    styleUrls: ['./public-stats.component.scss'],
})
export class RdioScannerPublicStatsComponent implements OnInit {
    stats: StatsResponse | undefined;
    loading = true;
    error = false;

    // Overview cards data
    overviewCards: { label: string; value: string | number; icon: string; color: string }[] = [];

    // Talkgroup units dialog
    selectedTalkgroup: StatsLastHourTalkgroup | null = null;
    talkgroupUnits: StatsTalkgroupUnit[] = [];
    loadingUnits = false;

    // Chart configurations
    hourlyChartType: ChartType = 'bar';
    hourlyChartData: ChartData<'bar'> = { labels: [], datasets: [] };
    hourlyChartOptions: ChartConfiguration['options'] = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: { display: false },
            title: { display: true, text: 'Average Calls by Hour of Day (Last 7 Days)', color: '#e0e0e0' },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { beginAtZero: true, ticks: { color: '#a0a0a0', precision: 0 }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };

    // Time-range filter applied to the calls and listeners time-series
    // charts below. The rollup charts keep their fixed windows.
    range: StatsRange = '24h';
    ranges = STATS_RANGES;

    callsChartType: ChartType = 'line';
    callsChartData: ChartData<'line'> = { labels: [], datasets: [] };
    callsChartOptions: ChartConfiguration['options'] = timeSeriesOptions('Calls (Last 24 Hours)', false, COSMETICS);

    topChartType: ChartType = 'bar';
    topChartData: ChartData<'bar'> = { labels: [], datasets: [] };
    topChartOptions: ChartConfiguration['options'] = topSeriesOptions('Top Systems (Last 7 Days)');

    listenersChartType: ChartType = 'line';
    listenersChartData: ChartData<'line'> = { labels: [], datasets: [] };
    listenersChartOptions: ChartConfiguration['options'] = timeSeriesOptions('Listeners (Last 24 Hours)', true, COSMETICS);

    constructor(private http: HttpClient) {}

    ngOnInit(): void {
        this.loadStats();
    }

    async loadStats(): Promise<void> {
        this.loading = true;
        this.error = false;

        try {
            const url = `${window.location.href}/../api/stats`;
            this.stats = await firstValueFrom(this.http.get<StatsResponse>(url));
            if (this.stats) {
                this.buildOverviewCards();
                this.buildHourlyChart();
                this.buildTopChart();
                this.buildCallsChart();
                this.buildListenerCharts();
            }
        } catch (e) {
            console.error('Error loading stats:', e);
            this.error = true;
        } finally {
            this.loading = false;
        }
    }

    private _hourOfDayLast7d: number[] = new Array(24).fill(0);
    private _dayCountsLast30d: Map<string, number> = new Map();

    private buildOverviewCards(): void {
        if (!this.stats) return;

        const { overview } = this.stats;
        const buckets = this.stats.hourBuckets || [];

        // Bin UTC hour-buckets into local hour-of-day / day-of-period
        // so every derived overview value (Today, Week, Month, Avg/Day,
        // Peak Hour) reads in the browser's calendar.
        const now = new Date();
        const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        const startOfWeek = new Date(startOfToday); startOfWeek.setDate(startOfWeek.getDate() - 6);
        const startOfMonth = new Date(startOfToday); startOfMonth.setDate(startOfMonth.getDate() - 29);

        let todayCalls = 0;
        let weekCalls = 0;
        let monthCalls = 0;
        const hourOfDay = new Array<number>(24).fill(0);
        const dayCounts = new Map<string, number>();

        for (const b of buckets) {
            const t = new Date(b.startUtc);
            if (isNaN(t.getTime())) continue;
            if (t >= startOfToday) todayCalls += b.count;
            if (t >= startOfWeek) {
                weekCalls += b.count;
                hourOfDay[t.getHours()] += b.count;
            }
            if (t >= startOfMonth) {
                monthCalls += b.count;
                const key = `${t.getFullYear()}-${(t.getMonth() + 1).toString().padStart(2, '0')}-${t.getDate().toString().padStart(2, '0')}`;
                dayCounts.set(key, (dayCounts.get(key) || 0) + b.count);
            }
        }

        let peakHour = 0;
        let peakCount = -1;
        for (let h = 0; h < 24; h++) {
            if (hourOfDay[h] > peakCount) {
                peakCount = hourOfDay[h];
                peakHour = h;
            }
        }

        this._hourOfDayLast7d = hourOfDay;
        this._dayCountsLast30d = dayCounts;

        this.overviewCards = [
            { label: 'Total Calls', value: this.formatNumber(overview.totalCalls), icon: 'call', color: '#00bcd4' },
            { label: 'Today', value: this.formatNumber(todayCalls), icon: 'today', color: '#4caf50' },
            { label: 'This Week', value: this.formatNumber(weekCalls), icon: 'date_range', color: '#ff9800' },
            { label: 'This Month', value: this.formatNumber(monthCalls), icon: 'calendar_today', color: '#9c27b0' },
            { label: 'Active Systems', value: overview.activeSystems, icon: 'settings_input_antenna', color: '#2196f3' },
            { label: 'Active TGs', value: overview.activeTalkgroups, icon: 'groups', color: '#e91e63' },
            { label: 'Avg/Day', value: Math.round(monthCalls / 30), icon: 'trending_up', color: '#607d8b' },
            { label: 'Peak Hour', value: this.formatHour(peakHour), icon: 'schedule', color: '#795548' },
        ];
    }

    private buildHourlyChart(): void {
        const labels = this._hourOfDayLast7d.map((_, h) => `${h.toString().padStart(2, '0')}:00`);
        const data = this._hourOfDayLast7d;

        this.hourlyChartData = {
            labels,
            datasets: [{
                data,
                backgroundColor: 'rgba(0, 188, 212, 0.6)',
                borderColor: 'rgba(0, 188, 212, 1)',
                borderWidth: 1,
            }],
        };
    }

    setRange(range: StatsRange): void {
        if (this.range === range) {
            return;
        }
        this.range = range;
        this.buildCallsChart();
        this.buildListenerCharts();
    }

    private buildCallsChart(): void {
        if (!this.stats?.hourBuckets) return;

        const series = buildCallsSeries(
            this.range, this.stats.hourBuckets, this.stats.callFineBuckets, this._dayCountsLast30d,
        );
        this.callsChartOptions = timeSeriesOptions(series.title, false, COSMETICS);
        this.callsChartData = { labels: series.labels, datasets: callsDatasets(series) };
    }

    private buildTopChart(): void {
        // Prefer the option-aware ranking (by group/tag/system, following
        // Sort By Groups / Sort By Tags); fall back to plain top systems
        // when talking to a server that predates topCategories.
        const categories = this.stats?.topCategories?.length
            ? this.stats.topCategories
            : (this.stats?.topSystems || []).map(s => ({ label: s.systemLabel, count: s.count }));
        if (!categories.length) return;

        const series = buildTopSeries(categories, this.stats?.topCategoriesKind, COSMETICS);
        this.topChartOptions = topSeriesOptions(series.title);
        this.topChartData = { labels: series.labels, datasets: topDatasets(series) };
    }

    private buildListenerCharts(): void {
        const raw = this.stats?.listenerBuckets;
        if (!raw?.length) return;

        const series = buildListenersSeries(this.range, raw);
        this.listenersChartOptions = timeSeriesOptions(series.title, true, COSMETICS);
        this.listenersChartData = { labels: series.labels, datasets: listenersDatasets(series) };
    }

    private formatNumber(num: number): string {
        if (num >= 1000000) {
            return (num / 1000000).toFixed(1) + 'M';
        } else if (num >= 1000) {
            return (num / 1000).toFixed(1) + 'K';
        }
        return num.toString();
    }

    private formatHour(hour: number): string {
        const suffix = hour >= 12 ? 'PM' : 'AM';
        const displayHour = hour > 12 ? hour - 12 : (hour === 0 ? 12 : hour);
        return `${displayHour} ${suffix}`;
    }

    refresh(): void {
        this.loadStats();
    }

    async showTalkgroupUnits(talkgroup: StatsLastHourTalkgroup): Promise<void> {
        this.selectedTalkgroup = talkgroup;
        this.loadingUnits = true;
        this.talkgroupUnits = [];

        try {
            const url = `${window.location.href}/../api/stats/talkgroup-units?system=${talkgroup.systemId}&talkgroup=${talkgroup.talkgroupId}`;
            const units = await firstValueFrom(this.http.get<StatsTalkgroupUnit[]>(url));
            this.talkgroupUnits = units || [];
        } catch (e) {
            console.error('Failed to load talkgroup units:', e);
        } finally {
            this.loadingUnits = false;
        }
    }

    closeTalkgroupUnits(): void {
        this.selectedTalkgroup = null;
        this.talkgroupUnits = [];
    }

    formatTimeAgo(dateTimeStr: string): string {
        try {
            const callTime = new Date(dateTimeStr);
            const now = new Date();
            const diffMs = now.getTime() - callTime.getTime();
            const diffMins = Math.floor(diffMs / 60000);
            
            if (diffMins < 1) return 'Just now';
            if (diffMins < 60) return `${diffMins}m ago`;
            
            const diffHours = Math.floor(diffMins / 60);
            if (diffHours < 24) return `${diffHours}h ago`;
            
            const diffDays = Math.floor(diffHours / 24);
            return `${diffDays}d ago`;
        } catch {
            return '';
        }
    }
}


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

import { Component, OnInit } from '@angular/core';
import { ChartConfiguration, ChartData, ChartType } from 'chart.js';
import {
    STATS_RANGES, StatsChartCosmetics, StatsRange,
    buildCallsSeries, buildListenersSeries, buildTopSeries,
    callsDatasets, listenersDatasets, topDatasets,
    timeSeriesOptions, topSeriesOptions,
} from '../../stats/stats-charts';
import { RdioScannerAdminService, StatsResponse, StatsLastHourTalkgroup, StatsTalkgroupUnit } from '../admin.service';

const COSMETICS: StatsChartCosmetics = { maxTicksLimit: 12, truncateLabels: 28 };

@Component({
    selector: 'rdio-scanner-admin-stats',
    templateUrl: './stats.component.html',
    styleUrls: ['./stats.component.scss'],
})
export class RdioScannerAdminStatsComponent implements OnInit {
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

    constructor(private adminService: RdioScannerAdminService) {}

    ngOnInit(): void {
        this.loadStats();
    }

    async loadStats(): Promise<void> {
        this.loading = true;
        this.error = false;

        try {
            this.stats = await this.adminService.getStats();
            if (this.stats) {
                this.buildOverviewCards();
                this.buildHourlyChart();
                this.buildTopChart();
                this.buildCallsChart();
                this.buildListenerCharts();
            }
        } catch (e) {
            this.error = true;
        } finally {
            this.loading = false;
        }
    }

    private buildOverviewCards(): void {
        if (!this.stats) return;

        const { overview } = this.stats;
        const buckets = this.stats.hourBuckets || [];

        // Bin the buckets into hour-of-day / day-of-period buckets in
        // the browser's local timezone. Server side ships pure UTC.
        const now = new Date();
        const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        const startOfWeek = new Date(startOfToday); startOfWeek.setDate(startOfWeek.getDate() - 6);
        const startOfMonth = new Date(startOfToday); startOfMonth.setDate(startOfMonth.getDate() - 29);

        // Start of the selected filter range. 'all' has no lower bound, so
        // every bucket the server sent (30 days) counts.
        const rangeHours: { [key: string]: number } = {
            '1h': 1, '6h': 6, '12h': 12, '24h': 24, '48h': 48, '1w': 24 * 7, '1m': 24 * 30,
        };
        const rangeStart = this.range === 'all'
            ? new Date(0)
            : new Date(now.getTime() - (rangeHours[this.range] ?? 24) * 3600 * 1000);

        let todayCalls = 0;
        let weekCalls = 0;
        let monthCalls = 0;
        let rangeCalls = 0;
        const rangeDays = new Set<string>();
        const hourOfDay = new Array<number>(24).fill(0);
        const dayCounts = new Map<string, number>(); // local YYYY-MM-DD -> count

        for (const b of buckets) {
            const t = new Date(b.startUtc);
            if (isNaN(t.getTime())) continue;
            if (t >= startOfToday) todayCalls += b.count;
            if (t >= startOfWeek) weekCalls += b.count;
            if (t >= startOfMonth) {
                monthCalls += b.count;
                const key = `${t.getFullYear()}-${(t.getMonth() + 1).toString().padStart(2, '0')}-${t.getDate().toString().padStart(2, '0')}`;
                dayCounts.set(key, (dayCounts.get(key) || 0) + b.count);
            }
            // Hour-of-day rollup and the range card follow the selected
            // filter rather than a fixed week, so the whole dashboard
            // describes one period.
            if (t >= rangeStart) {
                hourOfDay[t.getHours()] += b.count;
                rangeCalls += b.count;
                rangeDays.add(`${t.getFullYear()}-${t.getMonth()}-${t.getDate()}`);
            }
        }

        // Peak hour = argmax(hourOfDay).
        let peakHour = 0;
        let peakCount = -1;
        for (let h = 0; h < 24; h++) {
            if (hourOfDay[h] > peakCount) {
                peakCount = hourOfDay[h];
                peakHour = h;
            }
        }

        // Averaged over the days the range actually spans, not a fixed 30.
        const avgPerDay = rangeDays.size ? rangeCalls / rangeDays.size : 0;

        // Stash the binned arrays for the chart builders.
        this._rangeDayCount = rangeDays.size;
        this._hourOfDayLast7d = hourOfDay;
        this._dayCountsLast30d = dayCounts;

        this.overviewCards = [
            { label: 'Total Calls', value: this.formatNumber(overview.totalCalls), icon: 'call', color: '#00bcd4' },
            { label: this.rangeLabel(), value: this.formatNumber(rangeCalls), icon: 'filter_alt', color: '#00bcd4' },
            { label: 'Today', value: this.formatNumber(todayCalls), icon: 'today', color: '#4caf50' },
            { label: 'This Week', value: this.formatNumber(weekCalls), icon: 'date_range', color: '#ff9800' },
            { label: 'This Month', value: this.formatNumber(monthCalls), icon: 'calendar_month', color: '#9c27b0' },
            { label: 'Active Systems', value: overview.activeSystems, icon: 'settings_input_antenna', color: '#2196f3' },
            { label: 'Active Talkgroups', value: overview.activeTalkgroups, icon: 'groups', color: '#e91e63' },
            { label: 'Avg/Day', value: Math.round(avgPerDay), icon: 'trending_up', color: '#607d8b' },
            { label: 'Peak Hour', value: this.formatHour(peakHour), icon: 'schedule', color: '#795548' },
            // Configured inventory, distinct from the activity counts above:
            // these count what exists in the config, not what's been heard.
            { label: 'Systems', value: this.stats.configuredSystems ?? 0, icon: 'podcasts', color: '#3f51b5' },
            { label: 'Talkgroups', value: this.formatNumber(this.stats.configuredTalkgroups ?? 0), icon: 'forum', color: '#009688' },
            { label: 'Units', value: this.formatNumber(this.stats.configuredUnits ?? 0), icon: 'badge', color: '#8bc34a' },
        ];
    }

    // Label for the range-scoped card, e.g. "LAST 24 HOURS".
    private rangeLabel(): string {
        return (STATS_RANGES.find((r) => r.key === this.range)?.label ?? '') + ' calls';
    }

    private _rangeDayCount = 1;
    private _hourOfDayLast7d: number[] = new Array(24).fill(0);
    private _dayCountsLast30d: Map<string, number> = new Map();

    private buildHourlyChart(): void {
        const rangeLabel = STATS_RANGES.find((r) => r.key === this.range)?.label ?? '';

        // Three modes, because one shape doesn't fit every window:
        //  1h        — a 5-minute timeline; hour-of-day bins would give one bar.
        //  ≤ 24h     — hour-of-day totals; each hour occurs once, so a total
        //              and an average are the same number.
        //  > 24h     — hour-of-day averaged over the days covered, otherwise
        //              a month's bars are just 30x a day's and unreadable.
        let labels: string[];
        let data: number[];
        let title: string;

        if (this.range === '1h') {
            const cutoff = Date.now() - 3600 * 1000;
            const slots = (this.stats?.callMicroBuckets ?? []).filter((b) => {
                const t = new Date(b.startUtc).getTime();
                return !isNaN(t) && t >= cutoff;
            });
            labels = slots.map((b) => new Date(b.startUtc).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }));
            data = slots.map((b) => b.count);
            title = 'Calls per 5 Minutes (1H)';
        } else {
            const days = this._rangeDayCount;
            const average = days > 1;
            labels = this._hourOfDayLast7d.map((_, h) => `${h.toString().padStart(2, '0')}:00`);
            data = average
                ? this._hourOfDayLast7d.map((v) => Math.round((v / days) * 10) / 10)
                : this._hourOfDayLast7d;
            title = `${average ? 'Average ' : ''}Calls by Hour of Day (${rangeLabel})`;
        }

        this.hourlyChartOptions = {
            ...this.hourlyChartOptions,
            plugins: {
                ...(this.hourlyChartOptions?.plugins ?? {}),
                title: { display: true, text: title, color: '#e0e0e0' },
            },
        };

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

        // Everything here is derived from buckets already in hand — changing
        // range costs no request. It used to refetch so the server could
        // re-rank per range, but that ran a group-by per range and could
        // saturate the connection pool, timing out unrelated requests.
        this.buildOverviewCards();
        this.buildHourlyChart();
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

        const series = buildTopSeries(categories, this.stats?.topCategoriesKind, COSMETICS, 'Last 7 Days');
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
            const units = await this.adminService.getTalkgroupUnits(talkgroup.systemId, talkgroup.talkgroupId);
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


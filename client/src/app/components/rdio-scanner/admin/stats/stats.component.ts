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
import { RdioScannerAdminService, StatsResponse, StatsLastHourTalkgroup, StatsTalkgroupUnit } from '../admin.service';

type StatsRange = '1h' | '6h' | '12h' | '24h' | '48h' | '1w' | '1m' | 'all';

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
            title: { display: true, text: 'Average Calls Per Hour (Aggregated over Last 7 Days)', color: '#e0e0e0' },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };

    // Time-range filter applied to the calls and listeners time-series
    // charts below. The rollup charts keep their fixed windows.
    range: StatsRange = '24h';
    ranges: { key: StatsRange; label: string }[] = [
        { key: '1h', label: '1H' },
        { key: '6h', label: '6H' },
        { key: '12h', label: '12H' },
        { key: '24h', label: '24H' },
        { key: '48h', label: '48H' },
        { key: '1w', label: '1W' },
        { key: '1m', label: '1M' },
        { key: 'all', label: 'ALL' },
    ];

    callsChartType: ChartType = 'line';
    callsChartData: ChartData<'line'> = { labels: [], datasets: [] };
    callsChartOptions: ChartConfiguration['options'] = this.timeSeriesOptions('Calls (Last 24 Hours)', false);

    systemsChartType: ChartType = 'doughnut';
    systemsChartData: ChartData<'doughnut'> = { labels: [], datasets: [] };
    systemsChartOptions: ChartConfiguration['options'] = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: { position: 'right', labels: { color: '#e0e0e0' } },
            title: { display: true, text: 'Top Systems (Last 7 Days)', color: '#e0e0e0' },
        },
    };

    listenersChartType: ChartType = 'line';
    listenersChartData: ChartData<'line'> = { labels: [], datasets: [] };
    listenersChartOptions: ChartConfiguration['options'] = this.timeSeriesOptions('Listeners (Last 24 Hours)', true);

    // Chart color palette
    private colors = [
        'rgba(0, 188, 212, 0.8)',   // Cyan
        'rgba(76, 175, 80, 0.8)',   // Green
        'rgba(255, 152, 0, 0.8)',   // Orange
        'rgba(156, 39, 176, 0.8)',  // Purple
        'rgba(244, 67, 54, 0.8)',   // Red
        'rgba(33, 150, 243, 0.8)',  // Blue
        'rgba(255, 235, 59, 0.8)', // Yellow
        'rgba(121, 85, 72, 0.8)',   // Brown
        'rgba(96, 125, 139, 0.8)', // Blue Grey
        'rgba(233, 30, 99, 0.8)',   // Pink
    ];

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
                this.buildSystemsChart();
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

        let todayCalls = 0;
        let weekCalls = 0;
        let monthCalls = 0;
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
            // Hour-of-day rollup uses the last 7 days, matching the
            // historic "Average Calls Per Hour (over 7 days)" chart.
            if (t >= startOfWeek) hourOfDay[t.getHours()] += b.count;
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

        const avgPerDay = monthCalls / 30;

        // Stash the binned arrays for the chart builders.
        this._hourOfDayLast7d = hourOfDay;
        this._dayCountsLast30d = dayCounts;

        this.overviewCards = [
            { label: 'Total Calls', value: this.formatNumber(overview.totalCalls), icon: 'call', color: '#00bcd4' },
            { label: 'Today', value: this.formatNumber(todayCalls), icon: 'today', color: '#4caf50' },
            { label: 'This Week', value: this.formatNumber(weekCalls), icon: 'date_range', color: '#ff9800' },
            { label: 'This Month', value: this.formatNumber(monthCalls), icon: 'calendar_month', color: '#9c27b0' },
            { label: 'Active Systems', value: overview.activeSystems, icon: 'settings_input_antenna', color: '#2196f3' },
            { label: 'Active Talkgroups', value: overview.activeTalkgroups, icon: 'groups', color: '#e91e63' },
            { label: 'Avg/Day', value: Math.round(avgPerDay), icon: 'trending_up', color: '#607d8b' },
            { label: 'Peak Hour', value: this.formatHour(peakHour), icon: 'schedule', color: '#795548' },
        ];
    }

    private _hourOfDayLast7d: number[] = new Array(24).fill(0);
    private _dayCountsLast30d: Map<string, number> = new Map();

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

    private rangeHours(): number {
        switch (this.range) {
            case '1h': return 1;
            case '6h': return 6;
            case '12h': return 12;
            case '24h': return 24;
            case '48h': return 48;
            case '1w': return 168;
            default: return 720;
        }
    }

    private rangeTitle(): string {
        switch (this.range) {
            case '1h': return 'Last Hour';
            case '6h': return 'Last 6 Hours';
            case '12h': return 'Last 12 Hours';
            case '24h': return 'Last 24 Hours';
            case '48h': return 'Last 48 Hours';
            case '1w': return 'Last 7 Days';
            case '1m': return 'Last 30 Days';
            default: return 'All Data';
        }
    }

    private timeSeriesOptions(title: string, legend: boolean): ChartConfiguration['options'] {
        return {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: { display: legend, labels: { color: '#e0e0e0' } },
                title: { display: true, text: title, color: '#e0e0e0' },
            },
            scales: {
                x: { ticks: { color: '#a0a0a0', maxTicksLimit: 12 }, grid: { color: 'rgba(255,255,255,0.1)' } },
                y: { beginAtZero: true, ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            },
        };
    }

    private buildCallsChart(): void {
        if (!this.stats?.hourBuckets) return;

        // Calls ship as hourly UTC buckets covering 30 days, so 'all' and
        // '1m' both render the full shipped window. Hour bins up to 48h,
        // day bins beyond — a month of hourly points is noise and an hour
        // of daily points is nothing.
        const hours = Math.min(this.rangeHours(), 720);
        const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        const labels: string[] = [];
        const data: number[] = [];

        if (hours <= 48) {
            // Hour bins with windowed matching — safe for half-hour-offset
            // timezones where a local hour boundary lands at :30 UTC.
            const buckets: { ms: number; count: number }[] = [];
            for (const b of this.stats.hourBuckets) {
                const t = new Date(b.startUtc);
                if (!isNaN(t.getTime())) buckets.push({ ms: t.getTime(), count: b.count });
            }

            const now = new Date();
            const currentLocalHour = new Date(
                now.getFullYear(), now.getMonth(), now.getDate(), now.getHours(),
            );

            const HOUR_MS = 3600000;
            for (let i = hours - 1; i >= 0; i--) {
                const slot = new Date(currentLocalHour);
                slot.setHours(slot.getHours() - i);
                const slotMs = slot.getTime();
                let count = 0;
                for (const b of buckets) {
                    if (b.ms >= slotMs && b.ms < slotMs + HOUR_MS) count += b.count;
                }
                labels.push(`${slot.getHours().toString().padStart(2, '0')}:00`);
                data.push(count);
            }
        } else {
            const today = new Date();
            const dayCount = Math.min(Math.round(hours / 24), 30);
            for (let i = dayCount - 1; i >= 0; i--) {
                const d = new Date(today.getFullYear(), today.getMonth(), today.getDate() - i);
                const key = `${d.getFullYear()}-${(d.getMonth() + 1).toString().padStart(2, '0')}-${d.getDate().toString().padStart(2, '0')}`;
                labels.push(`${months[d.getMonth()]} ${d.getDate()} (${days[d.getDay()]})`);
                data.push(this._dayCountsLast30d.get(key) || 0);
            }
        }

        const title = this.range === 'all' ? 'Last 30 Days' : this.rangeTitle();
        this.callsChartOptions = this.timeSeriesOptions(`Calls (${title})`, false);
        this.callsChartData = {
            labels,
            datasets: [{
                data,
                fill: true,
                backgroundColor: 'rgba(255, 152, 0, 0.2)',
                borderColor: 'rgba(255, 152, 0, 1)',
                tension: 0.3,
                pointRadius: 2,
                pointBackgroundColor: 'rgba(255, 152, 0, 1)',
            }],
        };
    }

    private buildSystemsChart(): void {
        if (!this.stats?.topSystems) return;

        const labels = this.stats.topSystems.map(s => s.systemLabel);
        const data = this.stats.topSystems.map(s => s.count);

        this.systemsChartData = {
            labels,
            datasets: [{
                data,
                backgroundColor: this.colors.slice(0, data.length),
                borderColor: 'rgba(48, 48, 48, 1)',
                borderWidth: 2,
            }],
        };
    }

    private buildListenerCharts(): void {
        // Listener buckets are sparse on purpose: an absent slot means the
        // server was down, not that nobody was listening. The dense axis is
        // built here, with nulls where no bucket exists so the line chart
        // shows gaps (spanGaps is off).
        const raw = this.stats?.listenerBuckets;
        if (!raw?.length) return;

        // Buckets are 10-minute UTC slots keyed by their start instant. An
        // instant renders the same in every timezone, so a direct map lookup
        // replaces local binning entirely — only the labels are local.
        const byMs = new Map<number, { avg: number; peak: number }>();
        let earliest = Number.MAX_SAFE_INTEGER;
        for (const b of raw) {
            const ms = new Date(b.startUtc).getTime();
            if (!isNaN(ms)) {
                byMs.set(ms, { avg: b.avg, peak: b.peak });
                if (ms < earliest) earliest = ms;
            }
        }

        const SLOT_MS = 600000;
        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        const nowSlot = Math.floor(Date.now() / SLOT_MS) * SLOT_MS;

        // 'all' stretches back to the earliest recorded sample (the server
        // ships its full 90-day retention); the other ranges are fixed
        // windows. Always 10-minute slots — that is the tracking grain.
        const slots = this.range === 'all'
            ? Math.max(1, Math.floor((nowSlot - earliest) / SLOT_MS) + 1)
            : this.rangeHours() * 6;

        const hhmm = (d: Date) =>
            `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
        const label = slots <= 288
            ? hhmm
            : (d: Date) => `${months[d.getMonth()]} ${d.getDate()} ${hhmm(d)}`;
        // Straight segments past 48h — bezier over thousands of points is
        // wasted work.
        const tension = slots <= 288 ? 0.3 : 0;

        const labels: string[] = [];
        const avgData: (number | null)[] = [];
        const peakData: (number | null)[] = [];
        for (let i = slots - 1; i >= 0; i--) {
            const ms = nowSlot - i * SLOT_MS;
            const b = byMs.get(ms);
            labels.push(label(new Date(ms)));
            avgData.push(b ? b.avg : null);
            peakData.push(b ? b.peak : null);
        }

        this.listenersChartOptions = this.timeSeriesOptions(`Listeners (${this.rangeTitle()})`, true);
        this.listenersChartData = {
            labels,
            datasets: [
                {
                    label: 'Average',
                    data: avgData,
                    fill: true,
                    backgroundColor: 'rgba(0, 188, 212, 0.2)',
                    borderColor: 'rgba(0, 188, 212, 1)',
                    tension,
                    pointRadius: 0,
                    spanGaps: false,
                },
                {
                    label: 'Peak',
                    data: peakData,
                    fill: false,
                    borderColor: 'rgba(255, 152, 0, 1)',
                    borderWidth: 1,
                    tension,
                    pointRadius: 0,
                    spanGaps: false,
                },
            ],
        };
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


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

interface StatsOverview {
    totalCalls: number;
    todayCalls: number;
    weekCalls: number;
    monthCalls: number;
    activeSystems: number;
    activeTalkgroups: number;
    avgCallsPerDay: number;
    peakHour: number;
}

interface StatsCallsByHour {
    hour: number;
    count: number;
}

interface StatsCallsByDay {
    date: string;
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

interface StatsResponse {
    overview: StatsOverview;
    callsByHour: StatsCallsByHour[];
    callsByDay: StatsCallsByDay[];
    topTalkgroups: StatsTopTalkgroup[];
    topSystems: StatsTopSystem[];
    topUnits: StatsTopUnit[];
    recentActivity: StatsCallsByHour[];
    lastHourTalkgroups: StatsLastHourTalkgroup[];
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
            title: { display: true, text: 'Average Calls Per Hour (Aggregated over Last 7 Days)', color: '#e0e0e0' },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };

    dailyChartType: ChartType = 'line';
    dailyChartData: ChartData<'line'> = { labels: [], datasets: [] };
    dailyChartOptions: ChartConfiguration['options'] = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: { display: false },
            title: { display: true, text: 'Calls Per Day (Last 30 Days)', color: '#e0e0e0' },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };

    systemsChartType: ChartType = 'doughnut';
    systemsChartData: ChartData<'doughnut'> = { labels: [], datasets: [] };
    systemsChartOptions: ChartConfiguration['options'] = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: { position: 'bottom', labels: { color: '#e0e0e0', boxWidth: 12, padding: 8 } },
            title: { display: true, text: 'Top Systems', color: '#e0e0e0' },
        },
    };

    recentChartType: ChartType = 'line';
    recentChartData: ChartData<'line'> = { labels: [], datasets: [] };
    recentChartOptions: ChartConfiguration['options'] = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
            legend: { display: false },
            title: { display: true, text: 'Calls Per Hour (Last 24 Hours)', color: '#e0e0e0' },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { ticks: { color: '#a0a0a0' }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };

    // Chart color palette
    private colors = [
        'rgba(0, 188, 212, 0.8)',
        'rgba(76, 175, 80, 0.8)',
        'rgba(255, 152, 0, 0.8)',
        'rgba(156, 39, 176, 0.8)',
        'rgba(244, 67, 54, 0.8)',
        'rgba(33, 150, 243, 0.8)',
        'rgba(255, 235, 59, 0.8)',
        'rgba(121, 85, 72, 0.8)',
        'rgba(96, 125, 139, 0.8)',
        'rgba(233, 30, 99, 0.8)',
    ];

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
                this.buildDailyChart();
                this.buildSystemsChart();
                this.buildRecentChart();
            }
        } catch (e) {
            console.error('Error loading stats:', e);
            this.error = true;
        } finally {
            this.loading = false;
        }
    }

    private buildOverviewCards(): void {
        if (!this.stats) return;

        const { overview } = this.stats;
        this.overviewCards = [
            { label: 'Total Calls', value: this.formatNumber(overview.totalCalls), icon: 'call', color: '#00bcd4' },
            { label: 'Today', value: this.formatNumber(overview.todayCalls), icon: 'today', color: '#4caf50' },
            { label: 'This Week', value: this.formatNumber(overview.weekCalls), icon: 'date_range', color: '#ff9800' },
            { label: 'This Month', value: this.formatNumber(overview.monthCalls), icon: 'calendar_month', color: '#9c27b0' },
            { label: 'Active Systems', value: overview.activeSystems, icon: 'settings_input_antenna', color: '#2196f3' },
            { label: 'Active TGs', value: overview.activeTalkgroups, icon: 'groups', color: '#e91e63' },
            { label: 'Avg/Day', value: Math.round(overview.avgCallsPerDay), icon: 'trending_up', color: '#607d8b' },
            { label: 'Peak Hour', value: this.formatHour(overview.peakHour), icon: 'schedule', color: '#795548' },
        ];
    }

    private buildHourlyChart(): void {
        if (!this.stats?.callsByHour) return;

        const labels = this.stats.callsByHour.map(h => `${h.hour.toString().padStart(2, '0')}:00`);
        const data = this.stats.callsByHour.map(h => h.count);

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

    private buildDailyChart(): void {
        if (!this.stats?.callsByDay) return;

        const labels = this.stats.callsByDay.map(d => {
            const date = new Date(d.date + 'T00:00:00');
            const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
            const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
            return `${months[date.getMonth()]} ${date.getDate()} (${days[date.getDay()]})`;
        });
        const data = this.stats.callsByDay.map(d => d.count);

        this.dailyChartData = {
            labels,
            datasets: [{
                data,
                fill: true,
                backgroundColor: 'rgba(76, 175, 80, 0.2)',
                borderColor: 'rgba(76, 175, 80, 1)',
                tension: 0.4,
                pointRadius: 3,
                pointBackgroundColor: 'rgba(76, 175, 80, 1)',
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

    private buildRecentChart(): void {
        if (!this.stats?.recentActivity) return;

        // Show actual hour times (chronologically from 24h ago to now)
        const labels = this.stats.recentActivity.map(h => `${h.hour.toString().padStart(2, '0')}:00`);
        const data = this.stats.recentActivity.map(h => h.count);

        this.recentChartData = {
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


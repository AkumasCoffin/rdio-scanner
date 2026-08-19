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

/*
 * Shared range/series logic for the admin and public stats views. Both
 * components render the same data with only cosmetic differences, so the
 * axis math, range handling, and series building live here once —
 * component-specific looks are passed in via StatsChartCosmetics.
 */

import { ChartConfiguration } from 'chart.js';

export type StatsRange = '1h' | '6h' | '12h' | '24h' | '48h' | '1w' | '1m' | 'all';

export const STATS_RANGES: { key: StatsRange; label: string }[] = [
    { key: '1h', label: '1H' },
    { key: '6h', label: '6H' },
    { key: '12h', label: '12H' },
    { key: '24h', label: '24H' },
    { key: '48h', label: '48H' },
    { key: '1w', label: '1W' },
    { key: '1m', label: '1M' },
    { key: 'all', label: 'ALL' },
];

// Structural shapes of the wire format — both components' StatsResponse
// fields satisfy these.
export interface HourBucketLike {
    startUtc: string;
    count: number;
}

export interface ListenerBucketLike {
    startUtc: string;
    avg: number;
    peak: number;
}

export interface TopCategoryLike {
    label: string;
    count: number;
}

export interface StatsChartCosmetics {
    maxTicksLimit: number;
    truncateLabels: number;
    legendBoxWidth?: number;
}

export const STATS_CHART_PALETTE = [
    'rgba(0, 188, 212, 0.8)',   // Cyan
    'rgba(76, 175, 80, 0.8)',   // Green
    'rgba(255, 152, 0, 0.8)',   // Orange
    'rgba(156, 39, 176, 0.8)',  // Purple
    'rgba(244, 67, 54, 0.8)',   // Red
    'rgba(33, 150, 243, 0.8)',  // Blue
    'rgba(255, 235, 59, 0.8)',  // Yellow
    'rgba(121, 85, 72, 0.8)',   // Brown
    'rgba(96, 125, 139, 0.8)',  // Blue Grey
    'rgba(233, 30, 99, 0.8)',   // Pink
];

const SLOT_MS = 600000;
const HOUR_MS = 3600000;
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

const hhmm = (d: Date) =>
    `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
// Windows longer than a day need the date in the label — time-only labels
// repeat across days and make ticks/tooltips ambiguous.
const dateTime = (d: Date) => `${MONTHS[d.getMonth()]} ${d.getDate()} ${hhmm(d)}`;

export function rangeHours(range: StatsRange): number {
    switch (range) {
        case '1h': return 1;
        case '6h': return 6;
        case '12h': return 12;
        case '24h': return 24;
        case '48h': return 48;
        case '1w': return 168;
        default: return 720;
    }
}

export function rangeTitle(range: StatsRange): string {
    switch (range) {
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

export function timeSeriesOptions(
    title: string, legend: boolean, cosmetics: StatsChartCosmetics,
): ChartConfiguration['options'] {
    const legendLabels: { color: string; boxWidth?: number; padding?: number } = { color: '#e0e0e0' };
    if (cosmetics.legendBoxWidth) {
        legendLabels.boxWidth = cosmetics.legendBoxWidth;
        legendLabels.padding = 8;
    }
    return {
        responsive: true,
        maintainAspectRatio: false,
        // Hovering anywhere in a column shows every series at that x. Without
        // it Chart.js wants the cursor on the line itself, which is
        // unhittable where the series is drawn with no point markers.
        interaction: { mode: 'index', intersect: false },
        plugins: {
            legend: { display: legend, labels: legendLabels },
            title: { display: true, text: title, color: '#e0e0e0' },
            tooltip: { mode: 'index', intersect: false },
        },
        scales: {
            x: { ticks: { color: '#a0a0a0', maxTicksLimit: cosmetics.maxTicksLimit }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { beginAtZero: true, ticks: { color: '#a0a0a0', precision: 0 }, grid: { color: 'rgba(255,255,255,0.1)' } },
        },
    };
}

export function topSeriesOptions(title: string): ChartConfiguration['options'] {
    // Horizontal bars: long system/group/tag labels read far better on a
    // y axis than crammed into a doughnut legend.
    return {
        indexAxis: 'y',
        responsive: true,
        maintainAspectRatio: false,
        interaction: { mode: 'nearest', intersect: false },
        plugins: {
            legend: { display: false },
            title: { display: true, text: title, color: '#e0e0e0' },
            tooltip: { intersect: false },
        },
        scales: {
            x: { beginAtZero: true, ticks: { color: '#a0a0a0', precision: 0 }, grid: { color: 'rgba(255,255,255,0.1)' } },
            y: { ticks: { color: '#e0e0e0' }, grid: { display: false } },
        },
    };
}

export interface CallsSeries {
    title: string;
    labels: string[];
    data: number[];
    pointRadius: number;
}

// buildCallsSeries picks the finest grain the data allows for the range:
// dense 10-minute buckets up to 48h, hourly bins as a fallback for servers
// that don't ship them, day bins beyond 48h.
export function buildCallsSeries(
    range: StatsRange,
    hourBuckets: HourBucketLike[],
    fineBuckets: HourBucketLike[] | undefined,
    dayCounts: Map<string, number>,
): CallsSeries {
    const hours = rangeHours(range);
    const labels: string[] = [];
    const data: number[] = [];

    if (hours <= 48 && fineBuckets?.length) {
        const byMs = new Map<number, number>();
        let newest = 0;
        for (const b of fineBuckets) {
            const ms = new Date(b.startUtc).getTime();
            if (!isNaN(ms)) {
                byMs.set(ms, b.count);
                if (ms > newest) newest = ms;
            }
        }

        // Anchor the axis to the newest server bucket, not the client
        // clock — the response can be a couple of minutes stale, and a
        // clock-anchored axis paints that staleness as false zeros on the
        // right edge.
        const label = hours <= 24 ? hhmm : dateTime;
        const slots = hours * 6;
        for (let i = slots - 1; i >= 0; i--) {
            const ms = newest - i * SLOT_MS;
            labels.push(label(new Date(ms)));
            data.push(byMs.get(ms) || 0);
        }
        return { title: `Calls (${rangeTitle(range)})`, labels, data, pointRadius: 0 };
    }

    if (hours <= 48) {
        // Hourly fallback. Widen very short windows to 6 hours — an
        // hourly "series" over one hour is a single dot that reads as
        // "no data"; the title reflects the window actually drawn.
        const binHours = Math.max(hours, 6);
        const buckets: { ms: number; count: number }[] = [];
        for (const b of hourBuckets) {
            const t = new Date(b.startUtc);
            if (!isNaN(t.getTime())) buckets.push({ ms: t.getTime(), count: b.count });
        }

        // Windowed matching — safe for half-hour-offset timezones where a
        // local hour boundary lands at :30 UTC.
        const now = new Date();
        const currentLocalHour = new Date(
            now.getFullYear(), now.getMonth(), now.getDate(), now.getHours(),
        );
        const label = binHours <= 24 ? (d: Date) => `${d.getHours().toString().padStart(2, '0')}:00` : dateTime;
        for (let i = binHours - 1; i >= 0; i--) {
            const slot = new Date(currentLocalHour);
            slot.setHours(slot.getHours() - i);
            const slotMs = slot.getTime();
            let count = 0;
            for (const b of buckets) {
                if (b.ms >= slotMs && b.ms < slotMs + HOUR_MS) count += b.count;
            }
            labels.push(label(slot));
            data.push(count);
        }
        const title = binHours === hours ? rangeTitle(range) : `Last ${binHours} Hours`;
        return { title: `Calls (${title})`, labels, data, pointRadius: 2 };
    }

    // Longer windows still render from the hourly buckets, binned just
    // coarsely enough to keep the point count sane. Day bins gave a week
    // seven points, which the line's curve then smoothed into a wave —
    // every daily peak and quiet spell in the data was invisible.
    // Calls ship as a 30-day window, so 'all' and '1m' both render that.
    const spanHours = Math.min(hours, 30 * 24);
    const binHours = spanHours <= 7 * 24 ? 1 : 2;

    const byMs = new Map<number, number>();
    for (const b of hourBuckets) {
        const t = new Date(b.startUtc);
        if (!isNaN(t.getTime())) byMs.set(t.getTime(), b.count);
    }

    // The slot grid must line up with the buckets' UTC hour starts, so it is
    // derived from the epoch, not from the local clock's hour. In a
    // half-hour-offset timezone the local hour starts between UTC hours, and
    // a grid built from it would miss every bucket — the whole chart zero.
    const currentHour = Math.floor(Date.now() / HOUR_MS) * HOUR_MS;

    for (let i = Math.floor(spanHours / binHours) - 1; i >= 0; i--) {
        const slotMs = currentHour - i * binHours * HOUR_MS;
        let count = 0;
        for (let h = 0; h < binHours; h++) {
            count += byMs.get(slotMs + h * HOUR_MS) || 0;
        }
        labels.push(dateTime(new Date(slotMs)));
        data.push(count);
    }

    const title = range === 'all' ? 'Last 30 Days' : rangeTitle(range);
    return { title: `Calls (${title})`, labels, data, pointRadius: 0 };
}

export function callsDatasets(series: CallsSeries): ChartConfiguration<'line'>['data']['datasets'] {
    return [{
        data: series.data,
        fill: true,
        backgroundColor: 'rgba(255, 152, 0, 0.2)',
        borderColor: 'rgba(255, 152, 0, 1)',
        // Only smooth a sparse series. On a dense one the curve rounds real
        // peaks away, which is the difference between "quiet afternoon" and
        // "nothing happened".
        tension: series.data.length > 60 ? 0 : 0.3,
        pointRadius: series.pointRadius,
        pointBackgroundColor: 'rgba(255, 152, 0, 1)',
    }];
}

export interface ListenersSeries {
    title: string;
    labels: string[];
    avg: (number | null)[];
    peak: (number | null)[];
    tension: number;
}

// listenerBinMs — display bin per range. The tracking grain is 10 minutes,
// but a week or more of 10-minute points renders as unreadable spiky noise
// (1,000-13,000 points), so longer windows aggregate into coarser bins:
// every range lands at a readable 144-360 points.
function listenerBinMs(range: StatsRange): number {
    switch (range) {
        case '1w': return HOUR_MS;          // 168 points
        case '1m': return 3 * HOUR_MS;      // 240 points
        case 'all': return 6 * HOUR_MS;     // ≤ 360 points over 90 days
        default: return SLOT_MS;            // 10-minute tracking grain
    }
}

// buildListenersSeries renders the sparse listener buckets on a dense axis
// with nulls where no bin has data — an absent bin means the server was
// down, not that nobody was listening, and the null renders as a gap
// (spanGaps is off). Avg averages the bucket averages in the bin; Peak
// keeps the bin maximum, so spikes survive the decimation.
export function buildListenersSeries(range: StatsRange, buckets: ListenerBucketLike[]): ListenersSeries {
    const byMs = new Map<number, { avg: number; peak: number }>();
    let earliest = Number.MAX_SAFE_INTEGER;
    let newest = 0;
    for (const b of buckets) {
        const ms = new Date(b.startUtc).getTime();
        if (!isNaN(ms)) {
            byMs.set(ms, { avg: b.avg, peak: b.peak });
            if (ms < earliest) earliest = ms;
            if (ms > newest) newest = ms;
        }
    }

    const labels: string[] = [];
    const avg: (number | null)[] = [];
    const peak: (number | null)[] = [];

    if (!byMs.size) {
        return { title: `Listeners (${rangeTitle(range)})`, labels, avg, peak, tension: 0 };
    }

    const binMs = listenerBinMs(range);
    const byBin = new Map<number, { sum: number; n: number; peak: number }>();
    for (const [ms, b] of byMs) {
        const bin = ms - ms % binMs;
        const t = byBin.get(bin) || { sum: 0, n: 0, peak: 0 };
        t.sum += b.avg;
        t.n++;
        if (b.peak > t.peak) t.peak = b.peak;
        byBin.set(bin, t);
    }

    // Anchor to the newest sample, not the client clock — the response can
    // be a couple of minutes stale, and a clock-anchored axis renders that
    // staleness as a false "server down" gap on the right edge. 'all'
    // stretches back to the earliest sample instead of a fixed window.
    const newestBin = newest - newest % binMs;
    const start = range === 'all'
        ? earliest - earliest % binMs
        : newestBin - rangeHours(range) * HOUR_MS + binMs;

    const windowHours = (newestBin - start + binMs) / HOUR_MS;
    const label = windowHours <= 24 ? hhmm : dateTime;

    for (let ms = start; ms <= newestBin; ms += binMs) {
        const t = byBin.get(ms);
        labels.push(label(new Date(ms)));
        avg.push(t ? Math.round(t.sum / t.n * 100) / 100 : null);
        peak.push(t ? t.peak : null);
    }
    return { title: `Listeners (${rangeTitle(range)})`, labels, avg, peak, tension: 0.3 };
}

export function listenersDatasets(series: ListenersSeries): ChartConfiguration<'line'>['data']['datasets'] {
    return [
        {
            label: 'Average',
            data: series.avg,
            fill: true,
            backgroundColor: 'rgba(0, 188, 212, 0.2)',
            borderColor: 'rgba(0, 188, 212, 1)',
            tension: series.tension,
            pointRadius: 0,
            spanGaps: false,
        },
        {
            label: 'Peak',
            data: series.peak,
            fill: false,
            borderColor: 'rgba(255, 152, 0, 1)',
            borderWidth: 1,
            tension: series.tension,
            pointRadius: 0,
            spanGaps: false,
        },
    ];
}

export interface TopSeries {
    title: string;
    labels: string[];
    data: number[];
}

export function buildTopSeries(
    categories: TopCategoryLike[], kind: string | undefined, cosmetics: StatsChartCosmetics,
    // Names the window the ranking covers. Empty on the public page, which
    // has no range filter and takes the server's default.
    rangeSuffix = '',
): TopSeries {
    const kindTitle = kind === 'groups' ? 'Groups' : kind === 'tags' ? 'Tags' : 'Systems';
    const max = cosmetics.truncateLabels;
    const truncate = (s: string) => s.length > max ? `${s.slice(0, max - 1)}…` : s;
    return {
        title: `Top ${kindTitle}${rangeSuffix ? ` (${rangeSuffix})` : ''}`,
        labels: categories.map(c => truncate(c.label)),
        data: categories.map(c => c.count),
    };
}

export function topDatasets(series: TopSeries): ChartConfiguration<'bar'>['data']['datasets'] {
    return [{
        data: series.data,
        backgroundColor: STATS_CHART_PALETTE.slice(0, series.data.length),
        borderWidth: 0,
        borderRadius: 4,
    }];
}

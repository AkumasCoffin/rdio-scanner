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

/**
 * The single source of truth for LED colors (issue #10).
 *
 * The first eight are the stock chuot/rdio-scanner colors — their names and
 * meanings are part of the wire format shared with upstream servers and
 * clients, so they must never be renamed or removed. Extended colors declare a
 * `fallback` onto one of the stock eight: that is what a stock client shows
 * for them (stock code ignores unknown names and lights the default green),
 * and what anything emitting an upstream-compatible config should translate
 * them to.
 */

export interface LedColor {
    name: string;
    hex: string;
    /** Stock color to substitute where only the upstream eight are understood. */
    fallback?: string;
}

export const LED_COLORS: LedColor[] = [
    // Stock chuot/rdio-scanner set — do not rename, remove or reorder.
    { name: 'blue', hex: '#3b82f6' },
    { name: 'cyan', hex: '#06b6d4' },
    { name: 'green', hex: '#22c55e' },
    { name: 'magenta', hex: '#a855f7' },
    { name: 'orange', hex: '#f97316' },
    { name: 'red', hex: '#ef4444' },
    { name: 'white', hex: '#f9fafb' },
    { name: 'yellow', hex: '#eab308' },
    // Extended set — unknown to stock clients, which fall back gracefully.
    { name: 'lime', hex: '#84cc16', fallback: 'green' },
    { name: 'pink', hex: '#ec4899', fallback: 'magenta' },
    { name: 'teal', hex: '#14b8a6', fallback: 'cyan' },
    { name: 'violet', hex: '#7c3aed', fallback: 'magenta' },
];

/** All selectable color names, in display order. */
export const LED_NAMES: string[] = LED_COLORS.map((color) => color.name);

/** Name → hex, for anywhere that paints with the color directly. */
export const LED_HEX: { [name: string]: string } = LED_COLORS.reduce(
    (map, color) => ({ ...map, [color.name]: color.hex }),
    {} as { [name: string]: string },
);

/**
 * Resolves a configured led value to a known color name, or '' when unknown —
 * callers treat '' as "no color" and keep the default green LED, matching how
 * stock clients degrade when they meet a color they don't know.
 */
export function ledName(led: string | null | undefined): string {
    return led && LED_NAMES.includes(led) ? led : '';
}

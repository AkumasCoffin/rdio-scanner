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
 * CSV helpers for the admin Import/Export tools. RFC 4180 semantics: fields
 * are quoted when they contain commas, quotes, or newlines; embedded quotes
 * are doubled; quoted fields may span lines. Exports carry a UTF-8 BOM so
 * Excel detects the encoding; the parser strips it.
 */

// parseCsv parses text into rows of fields with a small state machine —
// the split-on-comma approach it replaces broke on any label containing
// a comma. Fully empty lines are dropped.
export function parseCsv(text: string): string[][] {
    if (text.charCodeAt(0) === 0xfeff) {
        text = text.slice(1);
    }

    const rows: string[][] = [];
    let row: string[] = [];
    let field = '';
    let inQuotes = false;

    const endField = () => {
        row.push(field);
        field = '';
    };
    const endRow = () => {
        endField();
        rows.push(row);
        row = [];
    };

    for (let i = 0; i < text.length; i++) {
        const c = text[i];
        if (inQuotes) {
            if (c === '"') {
                if (text[i + 1] === '"') {
                    field += '"';
                    i++;
                } else {
                    inQuotes = false;
                }
            } else {
                field += c;
            }
        } else if (c === '"') {
            inQuotes = true;
        } else if (c === ',') {
            endField();
        } else if (c === '\n') {
            endRow();
        } else if (c === '\r') {
            if (text[i + 1] === '\n') {
                i++;
            }
            endRow();
        } else {
            field += c;
        }
    }
    if (field !== '' || row.length) {
        endRow();
    }

    return rows.filter((r) => r.some((cell) => cell.trim() !== ''));
}

export function toCsv(rows: (string | number | null | undefined)[][]): string {
    const quote = (v: string | number | null | undefined): string => {
        const s = v === null || v === undefined ? '' : `${v}`;
        return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
    };
    return '﻿' + rows.map((r) => r.map(quote).join(',')).join('\r\n') + '\r\n';
}

export function downloadFile(document: Document, filename: string, mimeType: string, content: string): void {
    const url = URL.createObjectURL(new Blob([content], { type: mimeType }));

    const el = document.createElement('a');
    el.style.display = 'none';
    el.href = url;
    el.download = filename;

    document.body.appendChild(el);
    el.click();
    document.body.removeChild(el);

    URL.revokeObjectURL(url);
}

export function slugify(s: string): string {
    return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'export';
}

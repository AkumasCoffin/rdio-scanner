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

import { Component, Input } from '@angular/core';
import { FormGroup } from '@angular/forms';

@Component({
    selector: 'rdio-scanner-admin-options',
    templateUrl: './options.component.html',
})
export class RdioScannerAdminOptionsComponent {
    @Input() form: FormGroup | undefined;

    // Reading the provider via a getter (instead of inlining
    // form?.get('transcriptionProvider')?.value in every binding) ensures the
    // template re-evaluates predictably on each change-detection cycle when
    // the toggle group's FormControl is updated.
    get provider(): string {
        const v = this.form?.get('transcriptionProvider')?.value;
        return typeof v === 'string' && v.length > 0 ? v : 'groq';
    }

    // Model presets surfaced as autocomplete suggestions below each
    // provider's model field. The fields are still plain text inputs — these
    // are hints, not constraints. Users can type anything their backend
    // accepts.
    readonly groqModelPresets: ReadonlyArray<string> = [
        'whisper-large-v3-turbo',
        'whisper-large-v3',
    ];
    readonly openaiModelPresets: ReadonlyArray<string> = [
        'whisper-1',
    ];
    // Self-hosted covers whisper.cpp, openai-whisper-server, faster-whisper-
    // server, and any other OpenAI-compatible Whisper backend. Different
    // implementations expect different model identifiers, so the list is
    // intentionally broad.
    readonly whisperModelPresets: ReadonlyArray<string> = [
        'whisper-1',
        'whisper-large-v3-turbo',
        'whisper-large-v3',
        'whisper-large-v2',
        'whisper-medium',
        'whisper-small',
        'whisper-base',
        'whisper-tiny',
        'Systran/faster-whisper-large-v3-turbo',
        'Systran/faster-whisper-large-v3',
        'Systran/faster-whisper-medium',
        'Systran/faster-whisper-small',
    ];

    // filterPresets narrows a preset list to entries containing the current
    // form value (case-insensitive). Empty/no-value returns the full list
    // so the dropdown shows everything when the user first focuses the
    // field. Recomputed on each change-detection tick — the lists are
    // tiny so the perf is fine.
    private filterPresets(presets: ReadonlyArray<string>, controlName: string): string[] {
        const raw = this.form?.get(controlName)?.value;
        const q = typeof raw === 'string' ? raw.trim().toLowerCase() : '';
        if (!q) return [...presets];
        return presets.filter((m) => m.toLowerCase().includes(q));
    }

    filteredGroqModels(): string[] {
        return this.filterPresets(this.groqModelPresets, 'transcriptionModel');
    }
    filteredOpenAIModels(): string[] {
        return this.filterPresets(this.openaiModelPresets, 'transcriptionOpenAIModel');
    }
    filteredWhisperModels(): string[] {
        return this.filterPresets(this.whisperModelPresets, 'transcriptionWhisperModel');
    }
}

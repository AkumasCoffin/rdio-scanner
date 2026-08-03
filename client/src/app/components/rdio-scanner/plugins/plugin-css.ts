/*
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
 */

/*
 * The text side of the plugin style layer, kept apart from the service so it
 * can be tested without a DOM.
 *
 * These are small, and every one of them fails silently when it is wrong: a
 * mis-boosted selector produces valid CSS that simply never matches, which
 * looks exactly like a plugin whose styles were ignored.
 */

/**
 * Raises a selector to at least the specificity an encapsulated component rule
 * carries.
 *
 * Angular's emulated encapsulation rewrites a component's `.rdio-button` to
 * `.rdio-button[_ngcontent-abc]`, one class-level step above what a plugin
 * writes. Prefixing `:root` adds exactly that step, so the two tie on
 * specificity and the cascade falls through to source order — which the host
 * controls by keeping the plugin's stylesheet last.
 *
 * Handled a selector at a time, because a comma-separated list boosted as a
 * whole would only lift its first branch and leave the rest losing.
 */
export function boostSelector(selector: string): string {
    return String(selector ?? '')
        .split(',')
        .map((part) => {
            const trimmed = part.trim();

            if (!trimmed) {
                return '';
            }

            // Already rooted at the document element: `:root html` would be a
            // descendant of itself and match nothing, so repeat the pseudo-class
            // on the existing compound instead of prefixing a new one.
            if (/^(html|:root)\b/.test(trimmed)) {
                return trimmed.replace(/^(html|:root)/, '$1:root');
            }

            return `:root ${trimmed}`;
        })
        .filter(Boolean)
        .join(', ');
}

/**
 * Accepts a property under either spelling.
 *
 * `backgroundColor` is what a JavaScript author writes and `background-color`
 * is what CSS takes. Custom properties are passed through untouched: they are
 * case-sensitive, so `--surfacePanel` is a different property from
 * `--surface-panel` and converting one into the other would quietly write to
 * something nothing reads.
 */
export function cssProperty(name: string): string {
    const trimmed = String(name ?? '').trim();

    if (trimmed.startsWith('--')) {
        return trimmed;
    }

    return trimmed.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`);
}

/**
 * The selector for a published anchor.
 *
 * Anchors are named without their attribute spelling everywhere a plugin
 * author meets them — `control-stats`, not `[data-rdio="control-stats"]` — so
 * the one place that has to know the spelling is here. A bare selector is still
 * accepted and passed through, since `ctx.ui` is sugar rather than a fence.
 */
export function anchorSelector(anchor: string): string {
    const trimmed = String(anchor ?? '').trim();

    if (!trimmed || /[[.#:\s>]/.test(trimmed)) {
        return trimmed;
    }

    return `[data-rdio="${trimmed}"]`;
}

/** One rule's declarations, in source order. */
export function cssDeclarations(
    properties: { [name: string]: string | number },
    important = false,
): string {
    return Object.entries(properties || {})
        .map(([name, value]) => `${cssProperty(name)}: ${value}${important ? ' !important' : ''}`)
        .join('; ');
}

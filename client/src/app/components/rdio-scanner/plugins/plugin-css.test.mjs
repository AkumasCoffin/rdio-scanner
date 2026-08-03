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
 * Run with: npm test
 *
 * These are the failure modes worth guarding, because none of them announce
 * themselves. A selector boosted wrongly is still valid CSS — it just never
 * matches anything, which from the outside is indistinguishable from a plugin
 * whose styles were ignored.
 */

import assert from 'node:assert/strict';
import test from 'node:test';

import { anchorSelector, boostSelector, cssDeclarations, cssProperty } from './plugin-css.ts';

test('an ordinary selector gains exactly one class-level step', () => {
    // One step is the whole point: it matches what Angular's emulated
    // encapsulation adds, so the plugin ties rather than overshooting into
    // territory the user's own theme cannot come back from.
    assert.equal(boostSelector('.rdio-button'), ':root .rdio-button');
    assert.equal(boostSelector('[data-rdio="lcd"]'), ':root [data-rdio="lcd"]');
});

test('every branch of a list is boosted, not just the first', () => {
    // Boosting the list as a whole would lift one branch and leave the others
    // losing — and the plugin would see half its rule apply.
    assert.equal(
        boostSelector('.a, .b, .c'),
        ':root .a, :root .b, :root .c',
    );
});

test('a selector already rooted at the document element still matches', () => {
    // `:root html` is html inside html, which is nothing at all. The repeat has
    // to land on the existing compound instead.
    assert.equal(boostSelector('html'), 'html:root');
    assert.equal(boostSelector(':root'), ':root:root');
    assert.equal(boostSelector('html.dark'), 'html:root.dark');
    assert.equal(boostSelector(':root[data-theme="dark"]'), ':root:root[data-theme="dark"]');
});

test('body is boosted as a descendant, because it is one', () => {
    assert.equal(boostSelector('body'), ':root body');
});

test('empty and ragged input does not produce a broken rule', () => {
    // A stray trailing comma in a plugin's selector should not emit `:root `
    // on its own, which would match the document element and restyle the page.
    assert.equal(boostSelector(''), '');
    assert.equal(boostSelector('  '), '');
    assert.equal(boostSelector('.a, , .b'), ':root .a, :root .b');
    assert.equal(boostSelector('.a,'), ':root .a');
});

test('properties are accepted under either spelling', () => {
    assert.equal(cssProperty('backgroundColor'), 'background-color');
    assert.equal(cssProperty('background-color'), 'background-color');
    assert.equal(cssProperty('borderTopLeftRadius'), 'border-top-left-radius');
});

test('custom properties are passed through untouched', () => {
    // They are case-sensitive: --surfacePanel and --surface-panel are different
    // properties, so converting one would write to something nothing reads.
    assert.equal(cssProperty('--surface-panel'), '--surface-panel');
    assert.equal(cssProperty('--surfacePanel'), '--surfacePanel');
});

test('declarations render in source order, with important opt-in', () => {
    const properties = { backgroundColor: '#000', color: '#0f0' };

    assert.equal(cssDeclarations(properties), 'background-color: #000; color: #0f0');
    assert.equal(
        cssDeclarations(properties, true),
        'background-color: #000 !important; color: #0f0 !important',
    );
});

test('numeric values survive', () => {
    // Written as numbers by anyone setting a z-index or an opacity.
    assert.equal(cssDeclarations({ zIndex: 10, opacity: 0 }), 'z-index: 10; opacity: 0');
});

test('an anchor name becomes its attribute selector', () => {
    assert.equal(anchorSelector('control-stats'), '[data-rdio="control-stats"]');
    assert.equal(anchorSelector('  lcd  '), '[data-rdio="lcd"]');
});

test('anything already a selector is left alone', () => {
    // ctx.ui is sugar, not a fence — a plugin that already has a selector in
    // hand should not have it mangled into an anchor name that matches nothing.
    for (const selector of ['.rdio-button', '#id', '[data-rdio="lcd"]', 'div > span', 'a:hover']) {
        assert.equal(anchorSelector(selector), selector);
    }
});

test('an empty anchor does not become a selector matching everything', () => {
    assert.equal(anchorSelector(''), '');
    assert.equal(anchorSelector(null), '');
});

// Extracts the theme contract from styles.scss into markdown.
//
// The contract is a promise to plugin authors, so it has to be documented — and
// a hand-written list of eighty custom properties would be wrong within a
// release. This reads the :root block that the application actually ships, which
// is the only copy that cannot drift from itself.
//
// Usage: node scripts/extract-theme-contract.mjs <output.md>

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const source = resolve(here, '..', 'src', 'styles.scss');
const target = process.argv[2];

if (!target) {
    console.error('usage: node scripts/extract-theme-contract.mjs <output.md>');
    process.exit(1);
}

const css = readFileSync(source, 'utf8');

const rootMatch = css.match(/:root\s*\{([\s\S]*?)\n\}/);
if (!rootMatch) {
    console.error(`no :root block found in ${source}`);
    process.exit(1);
}

// Match comments and declarations as whole tokens rather than reading the block
// line by line. Both can span lines — a grouping comment wraps, and
// --app-background is three lines of gradient — and a line-based parser drops
// exactly those, silently. A generated reference that quietly omits a property
// is worse than no reference at all, because nobody thinks to doubt it.
const TOKENS = /\/\*([\s\S]*?)\*\/|(--[a-z0-9-]+)\s*:\s*([^;]+);/g;

const groups = [];
let current = { title: 'General', entries: [] };

for (const token of rootMatch[1].matchAll(TOKENS)) {
    const [, comment, name, value] = token;

    if (comment !== undefined) {
        if (current.entries.length) groups.push(current);

        // Wrapped comments become one line, and only the first sentence is the
        // heading — the rest is reasoning that belongs in the stylesheet.
        const text = comment.replace(/\s+/g, ' ').trim();
        current = { title: text.split('. ')[0].replace(/\.$/, ''), entries: [] };
        continue;
    }

    current.entries.push({
        name,
        // Collapse a multi-line value so it fits a table cell.
        value: value.replace(/\s+/g, ' ').trim(),
    });
}

if (current.entries.length) groups.push(current);

const total = groups.reduce((sum, group) => sum + group.entries.length, 0);
const version = css.match(/--theme-contract:\s*(\d+)/)?.[1] ?? '?';

const lines = [
    '# Theme contract',
    '',
    '<!-- Generated from client/src/styles.scss by scripts/extract-theme-contract.mjs.',
    '     Do not edit by hand; change the stylesheet and regenerate. -->',
    '',
    `Version **${version}** — ${total} properties.`,
    '',
    'These CSS custom properties are the supported surface for restyling Rdio',
    'Scanner. Set them on `:root` and the interface follows. Nothing in the',
    'application hardcodes a colour that a theme would want to change, so a theme',
    'never has to out-specify component styles with `!important`.',
    '',
    'From a plugin:',
    '',
    '```js',
    "ctx.theme.apply({ accent: '#38bdf8', 'accent-strong': '#0ea5e9' })",
    '',
    "ctx.theme.get('accent')      // current value",
    "ctx.theme.version()          // contract version, check before applying",
    "ctx.theme.reset()            // drop every override",
    '```',
    '',
    'Names may be written with or without the leading `--`.',
    '',
    'The `-rgb` triplets exist because several surfaces are translucent gradients.',
    '`rgba()` cannot take a hex custom property, so anything needing its own alpha',
    'uses the channels: `rgba(var(--accent-rgb), 0.3)`.',
    '',
    '## Stability',
    '',
    'The names are a promise. A theme written today keeps working after an rdio',
    'update — that is the point of publishing a contract rather than leaving themes',
    'to fight component styles. Adding a property is a minor change; renaming or',
    'removing one bumps `--theme-contract`, which `ctx.theme.version()` reports.',
    '',
];

for (const group of groups) {
    lines.push(`## ${group.title}`, '', '| Property | Default |', '| --- | --- |');
    for (const entry of group.entries) {
        lines.push(`| \`${entry.name}\` | \`${entry.value}\` |`);
    }
    lines.push('');
}

writeFileSync(target, lines.join('\n'), 'utf8');

console.log(`${target} written — ${total} properties, contract version ${version}`);

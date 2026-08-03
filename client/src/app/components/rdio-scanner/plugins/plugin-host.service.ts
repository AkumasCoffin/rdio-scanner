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

import { Injectable, NgZone } from '@angular/core';
import { BehaviorSubject } from 'rxjs';
import { anchorSelector, boostSelector, cssDeclarations } from './plugin-css';

/**
 * The webapp is AOT-compiled and embedded in the server binary, so a plugin
 * cannot ship Angular components — there is no compiler at runtime to build
 * them. Plugin frontend code is therefore plain JavaScript that renders into
 * DOM containers the app hands it.
 *
 * This service is the whole contract: it loads each enabled plugin's script,
 * gives it an API bound to its own id, and brokers between plugin-owned DOM and
 * Angular's change detection.
 */

/** Bump when the shape of the API passed to plugins changes incompatibly. */
export const PLUGIN_API_VERSION = 1;

export interface PluginEntry {
    id: string;
    name: string;
    version: string;
    /** Script path, relative to the app base. */
    entry: string;
    /** Directory the plugin's assets are served from, relative to the app base. */
    base: string;
}

export interface PluginViewSpec {
    id: string;
    label: string;
    icon?: string;
    mount: (el: HTMLElement) => (() => void) | void;
}

/** A view contributed by a plugin, tagged with which plugin owns it. */
export interface RegisteredPluginView extends PluginViewSpec {
    pluginId: string;
    /** Unique across plugins: two plugins may both call their view "map". */
    key: string;
}

type SlotFactory = (el: HTMLElement, data?: unknown) => (() => void) | void;

interface SlotRegistration {
    pluginId: string;
    factory: SlotFactory;
}

interface WsRegistration {
    pluginId: string;
    handler: (payload: unknown) => void;
}

/**
 * The path the browser was asked for, captured when this module is first
 * evaluated — which is while the bundle loads, before Angular bootstraps and so
 * before the router can rewrite it.
 *
 * It has to be captured this early. Plugin code loads asynchronously, after the
 * server's config arrives, by which point the router has already resolved the
 * initial URL. A path only a plugin claims matches nothing at that moment, falls
 * through the catch-all to the home page, and the address is rewritten — so by
 * the time the plugin registers its route, the path it was opened at is gone.
 */
const INITIAL_PATH = (() => {
    if (typeof window === 'undefined' || !window.location) {
        return '';
    }

    let path = window.location.pathname || '';

    // Strip the base href when the app is served under a sub-path, since routes
    // are relative to it.
    const base = document.querySelector('base')?.getAttribute('href') || '/';
    if (base !== '/' && path.startsWith(base)) {
        path = path.slice(base.length);
    }

    return path.replace(/^\/+|\/+$/g, '');
})();

/**
 * The query string and fragment the page was opened with.
 *
 * Replayed alongside the path, because an overlay is routinely opened with
 * configuration in its URL — and a replay that dropped it silently handed the
 * plugin an empty `query` on every cold open.
 */
const INITIAL_SEARCH = (() => {
    if (typeof window === 'undefined' || !window.location) {
        return '';
    }

    return (window.location.search || '') + (window.location.hash || '');
})();

/** What a plugin page is handed when the router lands on it. */
export interface PluginPageContext {
    params: { [key: string]: string };
    query: { [key: string]: string };
}

interface PageRegistration {
    pluginId: string;
    path: string;
    mount: (container: HTMLElement, context: PluginPageContext) => (() => void) | void;
}

/**
 * Where a plugin's content goes relative to the element it matched.
 *
 * `append` was the only behaviour there used to be, which made whole categories
 * of request inexpressible: a button *beside* AVOID rather than inside it, a
 * banner above the history table, a replacement for a control rather than an
 * addition to it.
 */
export type DomPosition = 'append' | 'prepend' | 'before' | 'after' | 'replace';

/**
 * A plugin attaching to arbitrary page elements rather than a named slot.
 *
 * Slots are convenience anchors, not a boundary — plugin code runs with full
 * page privileges and could always have reached into the DOM itself. The
 * difference this makes is lifecycle: attachments are tracked, re-applied to
 * elements that appear later, and torn down when the plugin is disabled,
 * which hand-rolled querySelector code in a plugin would not do.
 */
interface DomAttachment {
    pluginId: string;
    selector: string;
    factory: SlotFactory;
    /** Elements already mounted, with their teardown. */
    mounted: Map<Element, (() => void) | undefined>;
    once: boolean;
    position: DomPosition;
    /**
     * Elements hidden by a `replace`, so teardown can bring them back. The
     * original is hidden rather than removed: removing a node Angular is still
     * rendering into means the next change detection writes into a detached
     * tree, and the element never comes back when the plugin is disabled.
     */
    replaced?: Map<Element, string>;
}

/**
 * A plugin changing an element that is already on the page, rather than adding
 * one to it.
 *
 * Kept separate from DomAttachment because the lifecycle is the opposite way
 * round: an attachment owns a node it created and removes it, while this one
 * borrows a node the application owns and must put it back as it found it.
 */
interface DomDecoration {
    pluginId: string;
    selector: string;
    decorate: (element: Element) => (() => void) | void;
    applied: Map<Element, (() => void) | undefined>;
}

/**
 * The admin session token, when this page has one.
 *
 * A plugin's admin panel has to be able to reach its own admin endpoint, and
 * those endpoints authenticate with rdio.admin.verifyToken — so without this a
 * plugin could render a settings form and then be refused by its own backend,
 * with no way to authenticate at all.
 *
 * Sent only when an admin is signed in. On the public scanner page there is no
 * token and nothing is added, so a plugin route that does not check one is
 * reached exactly as before.
 */
function pluginApiHeaders(): Record<string, string> {
    const token = window?.sessionStorage?.getItem('rdio-scanner-admin-token') || '';

    return token ? { Authorization: token } : {};
}

@Injectable()
export class RdioScannerPluginHostService {
    /** Views contributed by plugins, for the navigation to render. */
    readonly views = new BehaviorSubject<RegisteredPluginView[]>([]);

    private entries: PluginEntry[] = [];
    private loaded = new Set<string>();

    private slots = new Map<string, SlotRegistration[]>();
    private slotSubscribers = new Map<string, Set<(regs: SlotRegistration[]) => void>>();

    private eventHandlers = new Map<string, Map<string, ((payload: unknown) => void)[]>>();

    private wsHandlers = new Map<string, WsRegistration[]>();
    private wsSender?: (command: string, payload: unknown) => void;

    private attachments: DomAttachment[] = [];
    private decorations: DomDecoration[] = [];
    private observer?: MutationObserver;

    /** One stylesheet per plugin, and the rules in it, keyed by selector. */
    private styleSheets = new Map<string, HTMLStyleElement>();
    private styleRules = new Map<string, Map<string, string>>();
    /** Bulk CSS from injectCss, kept apart so setting a rule cannot erase it. */
    private styleBulk = new Map<string, string[]>();
    /** Everything that has to stay at the end of <head>, links included. */
    private keepLast = new Set<HTMLElement>();
    private headObserver?: MutationObserver;

    /** Last value of each event, replayed to handlers that register late. */
    private lastEvent = new Map<string, unknown>();

    private exposedConfig: { [key: string]: unknown } = {};

    /**
     * The live RdioScannerService. Handed over by the service itself rather than
     * injected, because the host is constructed first and injecting the other
     * way round would be a dependency cycle.
     */
    private app: unknown;

    constructor(private ngZone: NgZone) {
        this.installGlobal();
    }

    /** Called once by RdioScannerService so plugins can reach it. */
    setApp(app: unknown): void {
        this.app = app;
    }

    /**
     * Mirrors a piece of scanner state onto <body> as a data attribute, so a
     * plugin can respond to it in CSS alone.
     *
     * Livefeed on or off, holding, which panel is open, whether a call is
     * playing — all of it lives in Angular component fields, so a plugin that
     * wanted a style to follow it had to subscribe in JavaScript and toggle
     * classes by hand, for something the cascade does natively:
     *
     *     body[data-rdio-livefeed="on"] [data-rdio="led"] { … }
     *
     * Written outside the Angular zone. This runs on every call and every
     * livefeed change, and scheduling change detection for an attribute
     * nothing in the application reads would be pure cost.
     */
    setState(name: string, value: string | number | boolean | null | undefined): void {
        const attribute = `data-rdio-${name}`;

        this.ngZone.runOutsideAngular(() => {
            const body = document.body;

            if (!body) {
                return;
            }

            if (value === null || value === undefined || value === '') {
                body.removeAttribute(attribute);
                return;
            }

            const text = typeof value === 'boolean' ? (value ? '1' : '0') : String(value);

            // Only on a real change: MutationObservers watch this document, and
            // rewriting an identical value would wake every one of them for
            // nothing on each call.
            if (body.getAttribute(attribute) !== text) {
                body.setAttribute(attribute, text);
            }
        });
    }

    /**
     * Pages plugins have claimed, by path. Registered at runtime, which is why
     * the router config is rebuilt rather than declared: a plugin is not known
     * when the application's routes are defined.
     */
    private pages = new Map<string, PageRegistration>();

    private routeInstaller?: (paths: string[]) => void;

    /**
     * The path this page was opened at, before the router touched it — but only
     * while it is still worth replaying.
     *
     * Consumed when a claimed route actually matches it, not merely when some
     * route is installed. Clearing it on the first install was wrong: a plugin
     * registering two pages installs twice, and a second plugin installs again,
     * so a page opened at the second path found the value already spent and the
     * browser stayed parked on the home page.
     *
     * `matched` is the caller's decision because only the installer knows how
     * routes match, parameters included.
     */
    takeInitialPath(matched: (path: string) => boolean): string {
        if (!this.initialPath || !matched(this.initialPath)) {
            return '';
        }

        const path = this.initialPath;

        // Spent once it is used, so disabling and re-enabling a plugin later
        // cannot drag whoever is browsing back to that page.
        this.initialPath = '';

        return path;
    }

    /** Query and fragment as opened, so a replay does not drop them. */
    readonly initialSearch = INITIAL_SEARCH;

    private initialPath = INITIAL_PATH;

    /**
     * Handed in by the page module, which owns the router and the component that
     * hosts a plugin page. Keeping it a callback means this service does not
     * depend on the router or on any routed component, so it stays usable from
     * the stream overlay and anywhere else that does not have them.
     */
    setRouteInstaller(installer: (paths: string[]) => void): void {
        this.routeInstaller = installer;
        this.routeInstaller(Array.from(this.pages.keys()));
    }

    /** Mounts a claimed page into a container. Returns its teardown. */
    mountPage(path: string, container: HTMLElement, context: PluginPageContext): () => void {
        const registration = this.pages.get(path);
        if (!registration) {
            return () => undefined;
        }

        let cleanup: (() => void) | void;

        this.safely(() => {
            cleanup = registration.mount(container, context);
        });

        return () => {
            this.safely(() => {
                if (typeof cleanup === 'function') {
                    cleanup();
                }
            });
            container.innerHTML = '';
        };
    }

    hasPage(path: string): boolean {
        return this.pages.has(path);
    }

    /** Rebuilds the router config. No-op before the page module hands one in. */
    private installRoutes(): void {
        // Angular renders navigation, so this has to re-enter the zone.
        this.ngZone.run(() => this.routeInstaller?.(Array.from(this.pages.keys())));
    }

    /**
     * Called with the plugin list from the server config payload. Scripts are
     * loaded once; a plugin that disappears from the list has been disabled, so
     * its contributions are torn down.
     */
    sync(entries: PluginEntry[] | undefined): void {
        const next = entries || [];
        this.entries = next;

        const active = new Set(next.map((entry) => entry.id));

        for (const id of Array.from(this.loaded)) {
            if (!active.has(id)) {
                this.teardown(id);
            }
        }

        for (const entry of next) {
            if (!this.loaded.has(entry.id)) {
                this.load(entry);
            }
        }
    }

    /** Publishes an app event to plugin handlers. */
    emit(event: string, payload: unknown): void {
        this.lastEvent.set(event, payload);

        for (const byEvent of Array.from(this.eventHandlers.values())) {
            for (const handler of byEvent.get(event) || []) {
                this.safely(() => handler(payload));
            }
        }
    }

    /** Publishes an inbound websocket message to whichever plugin claimed it. */
    emitWebsocket(command: string, payload: unknown): void {
        for (const registration of this.wsHandlers.get(command) || []) {
            this.safely(() => registration.handler(payload));
        }
    }

    /** True when a plugin has claimed a websocket command. */
    handlesWebsocket(command: string): boolean {
        return (this.wsHandlers.get(command) || []).length > 0;
    }

    /** Wires the outbound websocket path. Set once by the main service. */
    setWebsocketSender(sender: (command: string, payload: unknown) => void): void {
        this.wsSender = sender;
    }

    setExposedConfig(config: { [key: string]: unknown }): void {
        this.exposedConfig = config || {};
    }

    /**
     * Subscribes a slot container to its registrations. Returns an unsubscribe
     * function. Used by the slot component rather than by plugins.
     */
    observeSlot(name: string, notify: (regs: SlotRegistration[]) => void): () => void {
        let subscribers = this.slotSubscribers.get(name);
        if (!subscribers) {
            subscribers = new Set();
            this.slotSubscribers.set(name, subscribers);
        }
        subscribers.add(notify);

        notify(this.slots.get(name) || []);

        return () => subscribers?.delete(notify);
    }

    /**
     * Mounts a plugin into every element matching a selector, now and as they
     * appear. Backed by a MutationObserver so rows added to the call history or
     * a search result list get the plugin's content too.
     */
    private attach(attachment: DomAttachment): void {
        this.attachments.push(attachment);
        this.ensureObserver();
        this.applyAttachment(attachment);
    }

    private decorateWith(decoration: DomDecoration): void {
        this.decorations.push(decoration);
        this.ensureObserver();
        this.applyDecoration(decoration);
    }

    private applyAttachment(attachment: DomAttachment): void {
        let targets: Element[];

        try {
            targets = Array.from(document.querySelectorAll(attachment.selector));
        } catch {
            console.error(`[rdio-scanner] plugin ${attachment.pluginId} used an invalid selector: ${attachment.selector}`);
            return;
        }

        for (const target of targets) {
            if (attachment.mounted.has(target)) {
                continue;
            }

            // Marked so a re-render that reuses the element doesn't stack a
            // second copy of the plugin's content inside it.
            if (attachment.once && attachment.mounted.size > 0) {
                break;
            }

            const child = document.createElement('div');
            child.dataset['rdioPlugin'] = attachment.pluginId;

            if (!this.place(attachment, target, child)) {
                continue;
            }

            let teardown: (() => void) | undefined;

            try {
                const result = attachment.factory(child);
                if (typeof result === 'function') {
                    teardown = result;
                }
            } catch (err) {
                console.error(`[rdio-scanner] plugin ${attachment.pluginId} failed to attach to ${attachment.selector}`, err);
            }

            attachment.mounted.set(target, teardown);
        }

        // Drop elements that have left the document, so teardown runs and the
        // map doesn't grow without bound as rows scroll through.
        for (const [element, teardown] of Array.from(attachment.mounted.entries())) {
            if (!element.isConnected) {
                this.safely(() => teardown?.());
                attachment.mounted.delete(element);
            }
        }
    }

    /**
     * Puts the plugin's node where the attachment asked for it.
     *
     * Reports whether it landed. `before`, `after` and `replace` all need the
     * match to have a parent — `before` on `<html>` has nowhere to go — and
     * silently appending inside instead would be worse than not mounting, since
     * the plugin would see content on the page in the wrong place and no error.
     */
    private place(attachment: DomAttachment, target: Element, child: HTMLElement): boolean {
        const parent = target.parentNode;

        switch (attachment.position) {
            case 'prepend':
                target.insertBefore(child, target.firstChild);
                return true;

            case 'before':
                if (!parent) {
                    break;
                }
                parent.insertBefore(child, target);
                return true;

            case 'after':
                if (!parent) {
                    break;
                }
                parent.insertBefore(child, target.nextSibling);
                return true;

            case 'replace':
                if (!parent) {
                    break;
                }
                parent.insertBefore(child, target.nextSibling);

                // Hidden, not removed. Angular still owns this node and keeps
                // rendering into it; detaching it means the next change
                // detection writes into a tree nothing is showing, and the
                // element cannot be brought back when the plugin is disabled.
                attachment.replaced = attachment.replaced || new Map();
                if (!attachment.replaced.has(target)) {
                    attachment.replaced.set(target, (target as HTMLElement).style.display);
                }
                (target as HTMLElement).style.display = 'none';
                return true;

            default:
                target.appendChild(child);
                return true;
        }

        console.error(
            `[rdio-scanner] plugin ${attachment.pluginId} cannot place content ${attachment.position} ` +
                `${attachment.selector}: it has no parent`,
        );

        return false;
    }

    private detach(attachment: DomAttachment): void {
        for (const [, teardown] of Array.from(attachment.mounted.entries())) {
            this.safely(() => teardown?.());
        }
        attachment.mounted.clear();

        // Anything a `replace` hid comes back, and comes back to the value it
        // had rather than to empty — the element may have had a display of its
        // own before the plugin ever touched it.
        for (const [element, display] of Array.from(attachment.replaced || [])) {
            (element as HTMLElement).style.display = display;
        }
        attachment.replaced?.clear();

        for (const node of Array.from(document.querySelectorAll(`div[data-rdio-plugin="${attachment.pluginId}"]`))) {
            node.remove();
        }
    }

    /**
     * Runs a plugin's decoration over every match, now and as matches appear.
     *
     * The teardown a decoration returns is the plugin's own undo. Nothing here
     * can infer it: the host cannot know that setting a class means removing
     * that class, or that rewriting text means restoring the old text, so the
     * contract is that a decoration returns the function that puts things back.
     */
    private applyDecoration(decoration: DomDecoration): void {
        let targets: Element[];

        try {
            targets = Array.from(document.querySelectorAll(decoration.selector));
        } catch {
            console.error(
                `[rdio-scanner] plugin ${decoration.pluginId} used an invalid selector: ${decoration.selector}`,
            );
            return;
        }

        for (const target of targets) {
            if (decoration.applied.has(target)) {
                continue;
            }

            let undo: (() => void) | undefined;

            try {
                const result = decoration.decorate(target);
                if (typeof result === 'function') {
                    undo = result;
                }
            } catch (err) {
                console.error(
                    `[rdio-scanner] plugin ${decoration.pluginId} failed to decorate ${decoration.selector}`,
                    err,
                );
            }

            decoration.applied.set(target, undo);
        }

        // An element that has left the document is already gone as far as the
        // page is concerned, so its undo would be writing to a detached node —
        // dropped rather than run, so the map does not grow without bound as
        // history rows scroll through.
        for (const element of Array.from(decoration.applied.keys())) {
            if (!element.isConnected) {
                decoration.applied.delete(element);
            }
        }
    }

    private undecorate(decoration: DomDecoration): void {
        for (const [element, undo] of Array.from(decoration.applied.entries())) {
            if (element.isConnected) {
                this.safely(() => undo?.());
            }
        }
        decoration.applied.clear();
    }

    /**
     * One observer for every attachment. Batched through requestAnimationFrame
     * because Angular's change detection produces bursts of mutations and
     * re-querying per mutation would be wasteful.
     */
    private ensureObserver(): void {
        if (this.observer) {
            return;
        }

        let scheduled = false;

        this.observer = new MutationObserver(() => {
            if (scheduled) {
                return;
            }
            scheduled = true;
            requestAnimationFrame(() => {
                scheduled = false;
                for (const attachment of this.attachments) {
                    this.applyAttachment(attachment);
                }
                for (const decoration of this.decorations) {
                    this.applyDecoration(decoration);
                }
            });
        });

        this.ngZone.runOutsideAngular(() => {
            this.observer?.observe(document.body, { childList: true, subtree: true });
        });
    }

    /**
     * A plugin's stylesheet, created on first use and kept at the end of
     * <head>.
     *
     * Position is the whole point. Angular's emulated encapsulation gives a
     * component rule an extra `[_ngcontent-*]` attribute, so a plugin's plain
     * `.rdio-button` is out-specified before order is even consulted. Matching
     * that specificity only produces a tie — and a tie is settled by source
     * order, which a plugin cannot control: plugin code loads once the server's
     * config arrives, but the admin module is lazy-loaded and injects its styles
     * later, winning every tie on the one screen a plugin is most likely to be
     * styling.
     */
    private pluginStyleSheet(pluginId: string): HTMLStyleElement {
        let sheet = this.styleSheets.get(pluginId);

        if (!sheet) {
            sheet = document.createElement('style');
            sheet.dataset['rdioPluginStyles'] = pluginId;
            this.styleSheets.set(pluginId, sheet);
            this.trackLast(sheet);
        }

        return sheet;
    }

    /** Appends an element to <head> and keeps it there, at the end. */
    trackLast(element: HTMLElement): void {
        document.head.appendChild(element);
        this.keepLast.add(element);
        this.ensureHeadObserver();
    }

    /**
     * Moves plugin stylesheets back to the end of <head> whenever anything else
     * is added after them, so "last" stays true for the life of the page rather
     * than only at the moment the plugin loaded.
     */
    private ensureHeadObserver(): void {
        if (this.headObserver) {
            return;
        }

        let scheduled = false;

        this.headObserver = new MutationObserver(() => {
            if (scheduled) {
                return;
            }
            scheduled = true;
            requestAnimationFrame(() => {
                scheduled = false;
                this.keepStylesLast();
            });
        });

        this.ngZone.runOutsideAngular(() => {
            this.headObserver?.observe(document.head, { childList: true });
        });
    }

    private keepStylesLast(): void {
        for (const element of this.keepLast) {
            // Only when something actually follows it. Re-appending
            // unconditionally would retrigger the observer forever.
            if (element.parentNode === document.head && element.nextSibling) {
                document.head.appendChild(element);
            }
        }
    }

    setStyleRule(
        pluginId: string,
        selector: string,
        properties: { [name: string]: string | number } | null,
        important = false,
    ): void {
        const rules = this.styleRules.get(pluginId) || new Map<string, string>();
        this.styleRules.set(pluginId, rules);

        const boosted = boostSelector(selector);

        if (!boosted) {
            console.error(`[rdio-scanner] plugin ${pluginId} styles.set() needs a selector`);
            return;
        }

        // Null clears just this rule, so a plugin can undo one thing without
        // dropping everything else it has set.
        if (!properties) {
            rules.delete(boosted);
        } else {
            rules.set(boosted, cssDeclarations(properties, important));
        }

        this.renderStyles(pluginId);
    }

    clearStyles(pluginId: string): void {
        this.styleRules.delete(pluginId);
        this.styleBulk.delete(pluginId);
        this.renderStyles(pluginId);
    }

    private renderStyles(pluginId: string): void {
        const sheet = this.pluginStyleSheet(pluginId);

        // Bulk first, rules after, so a rule set through styles.set() overrides
        // the plugin's own bulk CSS at equal specificity rather than the order
        // depending on which call happened to come last.
        const parts = [...(this.styleBulk.get(pluginId) || [])];

        for (const [selector, body] of this.styleRules.get(pluginId) || []) {
            parts.push(`${selector} { ${body} }`);
        }

        sheet.textContent = parts.join('\n');

        this.keepStylesLast();
    }

    /** Bulk CSS goes into the same kept-last sheet, so it inherits the same win. */
    appendCss(pluginId: string, css: string): void {
        const bulk = this.styleBulk.get(pluginId) || [];
        bulk.push(css);
        this.styleBulk.set(pluginId, bulk);

        this.renderStyles(pluginId);
    }

    private notifySlot(name: string): void {
        const regs = this.slots.get(name) || [];
        for (const notify of Array.from(this.slotSubscribers.get(name) || [])) {
            this.safely(() => notify(regs));
        }
    }

    /**
     * Loads one plugin's script. Failures are logged and contained: a plugin
     * that throws must not take the scanner UI down with it.
     */
    private load(entry: PluginEntry): void {
        this.loaded.add(entry.id);

        const script = document.createElement('script');
        script.src = entry.entry;
        script.async = true;
        script.dataset['rdioPlugin'] = entry.id;

        script.onerror = () => {
            console.error(`[rdio-scanner] plugin ${entry.id} failed to load from ${entry.entry}`);
            this.loaded.delete(entry.id);
        };

        document.head.appendChild(script);
    }

    /** Removes everything a plugin contributed, when it is disabled. */
    private teardown(pluginId: string): void {
        this.loaded.delete(pluginId);
        this.eventHandlers.delete(pluginId);

        // Pages first. A route left pointing at a plugin that is gone renders an
        // empty page rather than a missing one, which reads as the application
        // being broken rather than the plugin being off.
        let droppedPage = false;
        for (const [path, registration] of Array.from(this.pages.entries())) {
            if (registration.pluginId === pluginId) {
                this.pages.delete(path);
                droppedPage = true;
            }
        }
        if (droppedPage) {
            this.installRoutes();
        }

        for (const [name, regs] of Array.from(this.slots.entries())) {
            const kept = regs.filter((reg) => reg.pluginId !== pluginId);
            if (kept.length !== regs.length) {
                this.slots.set(name, kept);
                this.notifySlot(name);
            }
        }

        // Only this plugin's handlers. Filtering everything out here meant
        // disabling one plugin silently deafened every other one.
        for (const [command, registrations] of Array.from(this.wsHandlers.entries())) {
            const kept = registrations.filter((registration) => registration.pluginId !== pluginId);
            if (kept.length) {
                this.wsHandlers.set(command, kept);
            } else {
                this.wsHandlers.delete(command);
            }
        }

        for (const attachment of this.attachments.filter((a) => a.pluginId === pluginId)) {
            this.detach(attachment);
        }
        this.attachments = this.attachments.filter((a) => a.pluginId !== pluginId);

        // Decorations before styles, so an element handed back to the
        // application is not left wearing a rule the plugin set on it.
        for (const decoration of this.decorations.filter((d) => d.pluginId === pluginId)) {
            this.undecorate(decoration);
        }
        this.decorations = this.decorations.filter((d) => d.pluginId !== pluginId);

        const views = this.views.value.filter((view) => view.pluginId !== pluginId);
        if (views.length !== this.views.value.length) {
            this.views.next(views);
        }

        for (const node of Array.from(document.querySelectorAll(`script[data-rdio-plugin="${pluginId}"]`))) {
            node.remove();
        }
        for (const node of Array.from(document.querySelectorAll(`style[data-rdio-plugin="${pluginId}"]`))) {
            node.remove();
        }

        // The style layer, and anything loadStyle put in head. Dropped from the
        // keep-last set as well as the document, or the observer would keep
        // trying to reposition nodes that are no longer anywhere.
        for (const selector of [`style[data-rdio-plugin-styles="${pluginId}"]`, `link[data-rdio-plugin="${pluginId}"]`]) {
            for (const node of Array.from(document.querySelectorAll(selector))) {
                this.keepLast.delete(node as HTMLElement);
                node.remove();
            }
        }

        this.styleSheets.delete(pluginId);
        this.styleRules.delete(pluginId);
        this.styleBulk.delete(pluginId);
    }

    /**
     * Installs window.rdioScanner.plugins. Plugin scripts call register() on it
     * as they load, so it has to exist before any of them run.
     */
    private installGlobal(): void {
        const host = this;

        const globalScope = window as unknown as { rdioScanner?: { [key: string]: unknown } };
        const root = globalScope.rdioScanner || (globalScope.rdioScanner = {});

        root['plugins'] = {
            apiVersion: PLUGIN_API_VERSION,

            register(pluginId: string, definition: { init?: (ctx: unknown) => void }): void {
                if (!pluginId || typeof definition?.init !== 'function') {
                    console.error('[rdio-scanner] plugin register() needs an id and an init function');
                    return;
                }

                const entry = host.entries.find((candidate) => candidate.id === pluginId);
                if (!entry) {
                    console.warn(`[rdio-scanner] plugin ${pluginId} registered but is not enabled on this server`);
                    return;
                }

                host.safely(() => definition.init!(host.contextFor(entry)));
            },
        };
    }

    /** Builds the API object one plugin sees, bound to its own identity. */
    private contextFor(entry: PluginEntry) {
        const host = this;
        const pluginId = entry.id;

        const assetUrl = (path: string) => `${entry.base}${String(path).replace(/^\/+/, '')}`;

        return {
            plugin: { id: entry.id, name: entry.name, version: entry.version },

            on(event: string, handler: (payload: unknown) => void): void {
                let byEvent = host.eventHandlers.get(pluginId);
                if (!byEvent) {
                    byEvent = new Map();
                    host.eventHandlers.set(pluginId, byEvent);
                }
                const handlers = byEvent.get(event) || [];
                handlers.push(handler);
                byEvent.set(event, handlers);

                // Replay the latest value so a plugin that loads after the
                // config arrived isn't left waiting for the next one.
                if (host.lastEvent.has(event)) {
                    host.safely(() => handler(host.lastEvent.get(event)));
                }
            },

            slots: {
                mount(name: string, factory: SlotFactory): void {
                    const regs = host.slots.get(name) || [];
                    regs.push({ pluginId, factory });
                    host.slots.set(name, regs);
                    host.notifySlot(name);
                },
            },

            /**
             * A whole page at a URL of the plugin's choosing, rendered with no
             * application chrome around it — the scanner's peer rather than
             * something inside it.
             *
             * This is what a feature the size of the stream overlay needs to
             * live in a plugin: its own top-level address, rendering nothing but
             * itself. A view cannot do it, because a view is always inside the
             * scanner.
             */
            routes: {
                register(spec: {
                    path: string;
                    mount: (container: HTMLElement, context: PluginPageContext) => (() => void) | void;
                }): void {
                    const path = String(spec?.path || '').replace(/^\/+|\/+$/g, '');

                    if (!path || typeof spec?.mount !== 'function') {
                        console.error(`[rdio-scanner] plugin ${pluginId} routes.register() needs a path and a mount function`);
                        return;
                    }

                    const existing = host.pages.get(path);
                    if (existing && existing.pluginId !== pluginId) {
                        console.error(
                            `[rdio-scanner] plugin ${pluginId} cannot claim /${path}; ${existing.pluginId} already has it`,
                        );
                        return;
                    }

                    host.pages.set(path, { pluginId, path, mount: spec.mount });
                    host.installRoutes();
                },
            },

            views: {
                register(spec: PluginViewSpec): void {
                    if (!spec?.id || typeof spec.mount !== 'function') {
                        console.error(`[rdio-scanner] plugin ${pluginId} views.register() needs an id and a mount function`);
                        return;
                    }

                    const view: RegisteredPluginView = {
                        ...spec,
                        pluginId,
                        key: `${pluginId}:${spec.id}`,
                    };

                    const views = host.views.value.filter((existing) => existing.key !== view.key);
                    views.push(view);

                    // Views change navigation, which Angular renders — so this
                    // one has to re-enter the zone.
                    host.ngZone.run(() => host.views.next(views));
                },
            },

            /**
             * Arbitrary placement. Slots are stable anchors the app promises to
             * keep; this is for everything else. Plugin code has full page
             * access regardless — what this adds is lifecycle: content is
             * re-applied to elements that appear later and removed when the
             * plugin is disabled.
             */
            dom: {
                attach(selector: string, factory: SlotFactory, options?: { position?: DomPosition }): void {
                    host.attach({
                        pluginId,
                        selector,
                        factory,
                        mounted: new Map(),
                        once: false,
                        position: options?.position || 'append',
                    });
                },
                /** Mounts into the first match only, e.g. a single overlay. */
                attachOnce(selector: string, factory: SlotFactory, options?: { position?: DomPosition }): void {
                    host.attach({
                        pluginId,
                        selector,
                        factory,
                        mounted: new Map(),
                        once: true,
                        position: options?.position || 'append',
                    });
                },
                /**
                 * Changes an element that is already there, rather than adding
                 * one — a class, an attribute, the text of a button.
                 *
                 * The function is handed the matched element itself, not a
                 * container inside it, which is the thing `attach` structurally
                 * cannot do. Return a function that puts the element back as it
                 * was: the host cannot infer that setting a class means removing
                 * it, so restoring on teardown is the plugin's to declare.
                 */
                decorate(selector: string, fn: (element: Element) => (() => void) | void): void {
                    host.decorateWith({ pluginId, selector, decorate: fn, applied: new Map() });
                },
                /** Escape hatch: the raw document, for anything the above cannot express. */
                document(): Document {
                    return document;
                },
            },

            ws: {
                on(command: string, handler: (payload: unknown) => void): void {
                    const key = String(command).toUpperCase();
                    const registrations = host.wsHandlers.get(key) || [];
                    registrations.push({ pluginId, handler });
                    host.wsHandlers.set(key, registrations);
                },
                send(command: string, payload: unknown): void {
                    host.wsSender?.(String(command).toUpperCase(), payload);
                },
            },

            api: {
                get(path: string): Promise<unknown> {
                    return fetch(`api/plugin/${pluginId}/${String(path).replace(/^\/+/, '')}`, {
                        headers: pluginApiHeaders(),
                    }).then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))));
                },
                post(path: string, body: unknown): Promise<unknown> {
                    return fetch(`api/plugin/${pluginId}/${String(path).replace(/^\/+/, '')}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json', ...pluginApiHeaders() },
                        body: JSON.stringify(body ?? {}),
                    }).then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))));
                },
            },

            config: {
                get(): { [key: string]: unknown } {
                    return { ...host.exposedConfig };
                },
            },

            assets: {
                url: assetUrl,
                loadScript(path: string): Promise<void> {
                    return new Promise((resolve, reject) => {
                        const el = document.createElement('script');
                        el.src = assetUrl(path);
                        el.async = true;
                        el.dataset['rdioPlugin'] = pluginId;
                        el.onload = () => resolve();
                        el.onerror = () => reject(new Error(`cannot load ${path}`));
                        document.head.appendChild(el);
                    });
                },
                loadStyle(path: string): Promise<void> {
                    return new Promise((resolve, reject) => {
                        const el = document.createElement('link');
                        el.rel = 'stylesheet';
                        el.href = assetUrl(path);
                        el.dataset['rdioPlugin'] = pluginId;
                        el.onload = () => resolve();
                        el.onerror = () => reject(new Error(`cannot load ${path}`));
                        // Tracked, so a plugin's own stylesheet gets the same
                        // guarantee its inline rules do: still last in <head>
                        // after the admin module lazy-loads its styles.
                        host.trackLast(el);
                    });
                },
            },

            injectCss(css: string): void {
                host.appendCss(pluginId, String(css || ''));
            },

            /**
             * Individual rules, in a stylesheet the host keeps at the end of
             * <head> and boosts to match an encapsulated component's
             * specificity — so a rule set here takes effect, which plain CSS
             * injected into the page does not reliably do.
             *
             * `important` stays opt-in. Making it the default would put plugin
             * styling beyond the reach of the user's own theme and leave two
             * plugins with no way to resolve a disagreement; the placement and
             * specificity above win without either cost.
             */
            styles: {
                set(
                    selector: string,
                    properties: { [name: string]: string | number } | null,
                    options?: { important?: boolean },
                ): void {
                    host.setStyleRule(pluginId, selector, properties, !!options?.important);
                },
                /** Drops one rule, or every rule this plugin has set. */
                clear(selector?: string): void {
                    if (selector) {
                        host.setStyleRule(pluginId, selector, null);
                        return;
                    }
                    host.clearStyles(pluginId);
                },
            },

            /**
             * The two things nearly every plugin doing UI work wants, by anchor
             * name rather than by selector.
             *
             * Both are a line of CSS or a decoration underneath, but neither
             * should require knowing that an anchor is spelled
             * `[data-rdio="…"]`, nor which of the two mechanisms is the right
             * one for the job.
             */
            ui: {
                /** Hides a built-in element. Reversible with show(). */
                hide(anchor: string): void {
                    host.setStyleRule(pluginId, anchorSelector(anchor), { display: 'none' });
                },
                show(anchor: string): void {
                    host.setStyleRule(pluginId, anchorSelector(anchor), null);
                },
                /**
                 * Rewrites an element's text, restoring the original when the
                 * plugin is disabled.
                 *
                 * Text rather than markup, deliberately: a plugin should not be
                 * injecting HTML into the application's own controls. The cost
                 * is that it replaces everything inside, so a two-line label
                 * built with a <br> — LIVE FEED, HOLD SYS — comes back as one
                 * line. Anything needing more structure than that wants
                 * `dom.decorate`, or `attach` with `position: 'replace'`.
                 */
                setLabel(anchor: string, label: string): void {
                    host.decorateWith({
                        pluginId,
                        selector: anchorSelector(anchor),
                        applied: new Map(),
                        decorate(element: Element) {
                            const original = element.textContent;
                            element.textContent = String(label);
                            return () => {
                                element.textContent = original;
                            };
                        },
                    });
                },
            },

            /**
             * The running scanner itself: livefeed, playback, avoid, presets,
             * search, hold, volume — the same object the app's own components
             * use, not a copy or a subset.
             *
             * Exposed whole deliberately. A curated wrapper would be a second
             * list to keep in step with the first, and the moment it fell behind
             * a plugin would be blocked on an rdio release for a method that
             * already existed. Anything a component can ask the scanner to do, a
             * plugin can too.
             */
            get app(): unknown {
                return host.app;
            },

            /**
             * The theme contract — the CSS custom properties declared on :root in
             * styles.scss.
             *
             * set() writes to the document root, which is where the contract is
             * defined, so a value set here wins over the stylesheet without any
             * plugin needing to out-specify component styles. That is the whole
             * reason the contract exists rather than leaving themes to fight
             * selectors with !important.
             */
            theme: {
                /** The contract version, so a theme can check before applying. */
                version(): number {
                    const raw = getComputedStyle(document.documentElement)
                        .getPropertyValue('--theme-contract')
                        .trim();
                    return Number(raw) || 0;
                },

                get(name: string): string {
                    return getComputedStyle(document.documentElement)
                        .getPropertyValue(host.themeProperty(name))
                        .trim();
                },

                set(name: string, value: string): void {
                    document.documentElement.style.setProperty(host.themeProperty(name), value);
                },

                /** Applies a whole theme at once. */
                apply(values: { [name: string]: string }): void {
                    for (const [name, value] of Object.entries(values || {})) {
                        document.documentElement.style.setProperty(host.themeProperty(name), value);
                    }
                },

                /** Drops overrides and falls back to the stylesheet's values. */
                reset(names?: string[]): void {
                    const root = document.documentElement;

                    if (names?.length) {
                        for (const name of names) {
                            root.style.removeProperty(host.themeProperty(name));
                        }
                        return;
                    }

                    // No names given: clear every property this page has set
                    // inline, which is exactly the set of theme overrides.
                    for (const property of Array.from(root.style)) {
                        if (property.startsWith('--')) {
                            root.style.removeProperty(property);
                        }
                    }
                },
            },
        };
    }

    /**
     * Accepts both `accent` and `--accent`. The contract is written with the
     * leading dashes, but a plugin author reading a theme out of JSON will
     * reasonably leave them off, and silently doing nothing would be a puzzle.
     */
    private themeProperty(name: string): string {
        const trimmed = String(name).trim();
        return trimmed.startsWith('--') ? trimmed : `--${trimmed}`;
    }

    /**
     * Runs plugin code without letting it break the app. Plugin frontend code
     * is third-party and runs with full page privileges by design, so the most
     * that can be done is stop an exception propagating into Angular.
     */
    private safely(fn: () => void): void {
        try {
            fn();
        } catch (err) {
            console.error('[rdio-scanner] plugin error', err);
        }
    }
}

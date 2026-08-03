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
    private observer?: MutationObserver;

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
            target.appendChild(child);

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

    private detach(attachment: DomAttachment): void {
        for (const [, teardown] of Array.from(attachment.mounted.entries())) {
            this.safely(() => teardown?.());
        }
        attachment.mounted.clear();

        for (const node of Array.from(document.querySelectorAll(`div[data-rdio-plugin="${attachment.pluginId}"]`))) {
            node.remove();
        }
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
            });
        });

        this.ngZone.runOutsideAngular(() => {
            this.observer?.observe(document.body, { childList: true, subtree: true });
        });
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
                attach(selector: string, factory: SlotFactory): void {
                    host.attach({ pluginId, selector, factory, mounted: new Map(), once: false });
                },
                /** Mounts into the first match only, e.g. a single overlay. */
                attachOnce(selector: string, factory: SlotFactory): void {
                    host.attach({ pluginId, selector, factory, mounted: new Map(), once: true });
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
                        document.head.appendChild(el);
                    });
                },
            },

            injectCss(css: string): void {
                const style = document.createElement('style');
                style.dataset['rdioPlugin'] = pluginId;
                style.textContent = css;
                document.head.appendChild(style);
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

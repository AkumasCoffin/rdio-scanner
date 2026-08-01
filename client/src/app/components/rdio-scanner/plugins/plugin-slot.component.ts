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

import {
    ChangeDetectionStrategy,
    Component,
    ElementRef,
    Input,
    NgZone,
    OnChanges,
    OnDestroy,
    OnInit,
    ViewChild,
} from '@angular/core';

import { RdioScannerPluginHostService } from './plugin-host.service';

/**
 * Renders whatever plugins have mounted into a named slot.
 *
 * Each plugin gets its own child element to own outright. Everything happens
 * outside the Angular zone: plugin DOM is not Angular's to track, and letting
 * a plugin's timers or listeners trigger change detection on every tick would
 * make the whole UI pay for it.
 */
@Component({
    changeDetection: ChangeDetectionStrategy.OnPush,
    selector: 'rdio-plugin-slot',
    template: '<div #container class="rdio-plugin-slot"></div>',
    styles: [
        `
            .rdio-plugin-slot {
                display: contents;
            }
        `,
    ],
})
export class RdioScannerPluginSlotComponent implements OnInit, OnChanges, OnDestroy {
    /** Slot name, matching what a plugin passes to ctx.slots.mount(). */
    @Input() name = '';

    /** Optional context handed to the plugin factory, e.g. the call for a row. */
    @Input() data: unknown;

    @ViewChild('container', { static: true }) container!: ElementRef<HTMLElement>;

    private unsubscribe?: () => void;
    private teardowns: (() => void)[] = [];
    private registrations: { pluginId: string; factory: (el: HTMLElement, data?: unknown) => (() => void) | void }[] = [];

    constructor(private ngZone: NgZone, private pluginHost: RdioScannerPluginHostService) {}

    ngOnInit(): void {
        this.unsubscribe = this.pluginHost.observeSlot(this.name, (regs) => {
            this.registrations = regs;
            this.render();
        });
    }

    ngOnChanges(): void {
        // data changed — plugins re-render against the new value.
        this.render();
    }

    ngOnDestroy(): void {
        this.unsubscribe?.();
        this.clear();
    }

    private clear(): void {
        for (const teardown of this.teardowns) {
            try {
                teardown();
            } catch (err) {
                console.error('[rdio-scanner] plugin slot teardown failed', err);
            }
        }
        this.teardowns = [];

        const el = this.container?.nativeElement;
        if (el) {
            while (el.firstChild) {
                el.removeChild(el.firstChild);
            }
        }
    }

    private render(): void {
        const host = this.container?.nativeElement;
        if (!host) {
            return;
        }

        this.ngZone.runOutsideAngular(() => {
            this.clear();

            for (const registration of this.registrations) {
                const child = document.createElement('div');
                child.dataset['rdioPlugin'] = registration.pluginId;
                host.appendChild(child);

                try {
                    const teardown = registration.factory(child, this.data);
                    if (typeof teardown === 'function') {
                        this.teardowns.push(teardown);
                    }
                } catch (err) {
                    console.error(`[rdio-scanner] plugin ${registration.pluginId} failed to render slot ${this.name}`, err);
                }
            }
        });
    }
}

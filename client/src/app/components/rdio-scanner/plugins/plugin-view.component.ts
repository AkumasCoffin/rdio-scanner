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
    ViewChild,
} from '@angular/core';

import { RdioScannerPluginHostService, RegisteredPluginView } from './plugin-host.service';

/**
 * Hosts a full-size, plugin-owned view.
 *
 * Slots let a plugin add to an existing screen; a view lets it own a whole one,
 * which is what anything substantial needs — a map, a dashboard, a live feed
 * from another service. The plugin gets an empty container and does as it likes
 * with it, outside the Angular zone.
 */
@Component({
    changeDetection: ChangeDetectionStrategy.OnPush,
    selector: 'rdio-plugin-view',
    template: '<div #container class="rdio-plugin-view"></div>',
    styles: [
        `
            .rdio-plugin-view {
                height: 100%;
                overflow: auto;
                width: 100%;
            }
        `,
    ],
})
export class RdioScannerPluginViewComponent implements OnChanges, OnDestroy {
    @Input() view?: RegisteredPluginView;

    @ViewChild('container', { static: true }) container!: ElementRef<HTMLElement>;

    private teardown?: () => void;
    private mounted?: string;

    constructor(private ngZone: NgZone, private pluginHost: RdioScannerPluginHostService) {}

    ngOnChanges(): void {
        if (this.view?.key === this.mounted) {
            return;
        }

        this.unmount();

        const view = this.view;
        const host = this.container?.nativeElement;

        if (!view || !host) {
            return;
        }

        this.mounted = view.key;

        this.ngZone.runOutsideAngular(() => {
            try {
                const teardown = view.mount(host);
                if (typeof teardown === 'function') {
                    this.teardown = teardown;
                }
            } catch (err) {
                console.error(`[rdio-scanner] plugin ${view.pluginId} failed to mount view ${view.id}`, err);
            }
        });
    }

    ngOnDestroy(): void {
        this.unmount();
    }

    private unmount(): void {
        if (this.teardown) {
            try {
                this.teardown();
            } catch (err) {
                console.error('[rdio-scanner] plugin view teardown failed', err);
            }
            this.teardown = undefined;
        }

        this.mounted = undefined;

        const host = this.container?.nativeElement;
        if (host) {
            while (host.firstChild) {
                host.removeChild(host.firstChild);
            }
        }
    }
}

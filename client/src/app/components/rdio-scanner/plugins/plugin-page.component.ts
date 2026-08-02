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

import {
    ChangeDetectionStrategy,
    Component,
    ElementRef,
    NgZone,
    OnDestroy,
    OnInit,
    ViewChild,
} from '@angular/core';
import { ActivatedRoute, Params } from '@angular/router';
import { combineLatest, Subscription } from 'rxjs';

import { RdioScannerPluginHostService } from './plugin-host.service';

/**
 * Hosts a whole page owned by a plugin, at a URL the plugin chose.
 *
 * Distinct from a plugin *view*, which renders inside the scanner. A page is the
 * scanner's peer: its sibling route renders under a bare router-outlet, so there
 * is no application chrome around it at all.
 *
 * That is what a feature like the /stream overlay needs to exist as a plugin —
 * its own top-level URL, rendering nothing but itself. Without this a plugin
 * could only ever add to a screen the application already owned.
 */
@Component({
    changeDetection: ChangeDetectionStrategy.OnPush,
    selector: 'rdio-scanner-plugin-page',
    template: '<div #container class="rdio-plugin-page"></div>',
    styles: [
        `
            .rdio-plugin-page {
                height: 100%;
                overflow: auto;
                width: 100%;
            }
        `,
    ],
})
export class RdioScannerPluginPageComponent implements OnInit, OnDestroy {
    @ViewChild('container', { static: true }) container!: ElementRef<HTMLElement>;

    private teardown?: () => void;
    private subscription?: Subscription;

    constructor(
        private ngZone: NgZone,
        private pluginHost: RdioScannerPluginHostService,
        private route: ActivatedRoute,
    ) {}

    ngOnInit(): void {
        // Remounted whenever the parameters or the query change, not just once.
        //
        // Angular reuses a component across navigations that hit the same route
        // config, so ngOnInit does not run again going from `unit/1` to
        // `unit/2` — the plugin would keep rendering the first one with nothing
        // to tell it otherwise. combineLatest fires once on subscribe, which is
        // the initial mount.
        this.subscription = combineLatest([this.route.params, this.route.queryParams])
            .subscribe(([params, query]) => this.remount(params, query));
    }

    private remount(params: Params, query: Params): void {
        // The path comes from the route's data rather than the URL, because a
        // registration may include parameters and the raw URL would not say
        // which registration matched.
        const path = this.route.snapshot.data['rdioPluginPath'] as string | undefined;
        if (!path) {
            return;
        }

        // Outside the zone: plugin code should not schedule change detection on
        // every listener it attaches to its own DOM.
        this.ngZone.runOutsideAngular(() => {
            const previous = this.teardown;
            this.teardown = undefined;

            if (previous) {
                previous();
            }

            this.teardown = this.pluginHost.mountPage(path, this.container.nativeElement, {
                params: { ...params },
                query: { ...query },
            });
        });
    }

    ngOnDestroy(): void {
        this.subscription?.unsubscribe();

        const teardown = this.teardown;
        this.teardown = undefined;

        if (teardown) {
            this.ngZone.runOutsideAngular(() => teardown());
        }
    }
}

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

import { FullscreenOverlayContainer, OverlayContainer } from '@angular/cdk/overlay';
import { HttpClientModule } from '@angular/common/http';
import { NgModule } from '@angular/core';
import { Route, Router } from '@angular/router';
import { NgChartsModule } from 'ng2-charts';
import { AppSharedModule } from '../../shared/shared.module';
import { RdioScannerComponent } from './rdio-scanner.component';
import { RdioScannerService } from './rdio-scanner.service';
import { RdioScannerMainComponent } from './main/main.component';
import { RdioScannerSupportComponent } from './main/support/support.component';
import { RdioScannerNativeModule } from './native/native.module';
import { RdioScannerPluginHostService } from './plugins/plugin-host.service';
import { RdioScannerPluginPageComponent } from './plugins/plugin-page.component';
import { RdioScannerPluginViewComponent } from './plugins/plugin-view.component';
import { RdioScannerSearchComponent } from './search/search.component';
import { RdioScannerSelectComponent } from './select/select.component';
import { RdioScannerPresetDialogComponent } from './select/preset-dialog.component';
import { RdioScannerPublicStatsComponent } from './stats/public-stats.component';
import { RdioScannerStreamComponent } from './stream/stream.component';

@NgModule({
    declarations: [
        RdioScannerComponent,
        RdioScannerMainComponent,
        RdioScannerPluginPageComponent,
        RdioScannerPluginViewComponent,
        RdioScannerPublicStatsComponent,
        RdioScannerSearchComponent,
        RdioScannerSelectComponent,
        RdioScannerPresetDialogComponent,
        RdioScannerStreamComponent,
        RdioScannerSupportComponent,
    ],
    exports: [RdioScannerComponent, RdioScannerStreamComponent],
    imports: [
        AppSharedModule,
        HttpClientModule,
        NgChartsModule,
        RdioScannerNativeModule,
    ],
    providers: [
        RdioScannerPluginHostService,
        RdioScannerService,
        { provide: OverlayContainer, useClass: FullscreenOverlayContainer },
    ],
})
export class RdioScannerModule {
    // The plugin route installer lives here because AppModule imports this
    // module, so this constructor actually runs. It was first put in
    // RdioScannerPageModule, which nothing imports — the plugin registered its
    // route correctly, the installer was never called, and the page silently
    // fell through to the home page with no error anywhere.
    constructor(router: Router, pluginHost: RdioScannerPluginHostService) {
        let installed = 0;

        pluginHost.setRouteInstaller((paths) => {
            // Nothing to add and nothing added before: leave the router exactly
            // as the application declared it. Every install without a
            // page-claiming plugin then never touches routing at all, so this
            // cannot regress navigation for anyone not using the feature.
            if (!paths.length && !installed) {
                return;
            }
            installed = paths.length;

            const config = router.config.map((route) => ({ ...route }));

            const parent = config.find((route) => route.path === '' && route.children);
            if (!parent?.children) {
                return;
            }

            // Rebuilt from the routes the application declared, so disabling a
            // plugin removes its page instead of accumulating stale ones.
            const declared = parent.children.filter((child) => !child.data?.['rdioPluginPath']);

            const claimed: Route[] = paths.map((path) => ({
                component: RdioScannerPluginPageComponent,
                data: { rdioPluginPath: path },
                path,
            }));

            // Plugin pages go last so a plugin cannot shadow a built-in route by
            // claiming its path — Angular matches in order.
            parent.children = [...declared, ...claimed];

            router.resetConfig(config);

            // The router resolved the initial URL long before this plugin
            // existed, so a path only a plugin claims was matched by the
            // catch-all and redirected home. Now that the route exists, go where
            // the browser was actually pointed.
            //
            // Matched against the route patterns rather than compared as
            // strings: a page registered as `unit/:id` opened at `unit/5` is
            // never equal to its own pattern, so a plain comparison meant every
            // parameterized plugin page failed to replay — a documented feature
            // that could not work from a cold open.
            const wanted = pluginHost.takeInitialPath((opened) => paths.some((pattern) => {
                const patternParts = pattern.split('/');
                const openedParts = opened.split('/');

                return patternParts.length === openedParts.length
                    && patternParts.every((part, i) => part.startsWith(':') || part === openedParts[i]);
            }));

            if (wanted) {
                const current = router.url.split(/[?#]/)[0].replace(/^\/+|\/+$/g, '');

                if (current !== wanted) {
                    // Query and fragment come along, since an overlay is often
                    // opened with its configuration in the URL.
                    router.navigateByUrl('/' + wanted + pluginHost.initialSearch);
                }
            }
        });
    }
}

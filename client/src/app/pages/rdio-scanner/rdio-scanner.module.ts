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

import { NgModule } from '@angular/core';
import { Route, Router } from '@angular/router';
import { RdioScannerModule } from '../../components/rdio-scanner';
import { RdioScannerPluginHostService } from '../../components/rdio-scanner/plugins/plugin-host.service';
import { AppSharedModule } from '../../shared/shared.module';
import { RdioScannerPageComponent } from './rdio-scanner.component';
import { RdioScannerMainPageComponent } from './rdio-scanner-main.component';
import { RdioScannerPluginPageComponent } from './rdio-scanner-plugin-page.component';
import { RdioScannerStreamPageComponent } from './rdio-scanner-stream.component';
import { routes } from './rdio-scanner.routes';

@NgModule({
    declarations: [
        RdioScannerPageComponent,
        RdioScannerMainPageComponent,
        RdioScannerPluginPageComponent,
        RdioScannerStreamPageComponent,
    ],
    exports: [RdioScannerPageComponent],
    imports: [
        RdioScannerModule,
        AppSharedModule.forChild({ routerRoutes: routes }),
    ],
})
export class RdioScannerPageModule {
    constructor(router: Router, pluginHost: RdioScannerPluginHostService) {
        // Plugins are not known when the routes above are declared, so pages
        // they claim have to be added afterwards. This is the only place that
        // can do it: the router is here, and so is the component that hosts one.
        let installed = 0;

        pluginHost.setRouteInstaller((paths) => {
            // Nothing to add and nothing added before: leave the router exactly
            // as the application declared it. Every install without a
            // page-claiming plugin — which is all of them today — then never
            // touches routing at all, so this cannot regress navigation for
            // anyone who is not using the feature.
            if (!paths.length && !installed) {
                return;
            }
            installed = paths.length;

            const config = router.config.map((route) => ({ ...route }));

            const parent = config.find((route) => route.path === '' && route.children);
            if (!parent?.children) {
                return;
            }

            // Rebuild from the routes the application declared, so a plugin
            // being disabled removes its page rather than accumulating stale
            // ones every time this runs.
            const declared = parent.children.filter((child) => !child.data?.['rdioPluginPath']);

            const claimed: Route[] = paths.map((path) => ({
                component: RdioScannerPluginPageComponent,
                data: { rdioPluginPath: path },
                path,
            }));

            // Plugin pages go last so a plugin cannot shadow a built-in route by
            // claiming its path — Angular matches in order, and '' would win
            // over nothing but itself.
            parent.children = [...declared, ...claimed];

            router.resetConfig(config);
        });
    }
}

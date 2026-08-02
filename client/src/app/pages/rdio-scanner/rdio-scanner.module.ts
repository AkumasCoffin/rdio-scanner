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
import { RdioScannerModule } from '../../components/rdio-scanner';
import { AppSharedModule } from '../../shared/shared.module';
import { RdioScannerPageComponent } from './rdio-scanner.component';
import { RdioScannerMainPageComponent } from './rdio-scanner-main.component';
import { RdioScannerStreamPageComponent } from './rdio-scanner-stream.component';
import { routes } from './rdio-scanner.routes';

// Nothing imports this module — the page components are reached through the
// route table instead. The plugin route installer deliberately does NOT live
// here: a constructor in a module no one imports never runs, which is exactly
// how the first attempt at plugin pages failed, silently and with the plugin
// itself registering perfectly. It lives in RdioScannerModule, which AppModule
// does import.
@NgModule({
    declarations: [
        RdioScannerPageComponent,
        RdioScannerMainPageComponent,
        RdioScannerStreamPageComponent,
    ],
    exports: [RdioScannerPageComponent],
    imports: [
        RdioScannerModule,
        AppSharedModule.forChild({ routerRoutes: routes }),
    ],
})
export class RdioScannerPageModule { }
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

import { Routes } from '@angular/router';
import { RdioScannerMainPageComponent } from './rdio-scanner-main.component';
import { RdioScannerPageComponent } from './rdio-scanner.component';

export const routes: Routes = [
    {
        path: '',
        component: RdioScannerPageComponent,
        children: [
            {
                component: RdioScannerMainPageComponent,
                path: '',
            },
            // No `stream` page here. The OBS overlay is the stream plugin's
            // now, and a route declared in this list is matched before any
            // page a plugin claims — so while one was, the plugin's overlay
            // could never appear at /stream, installed or not.
            {
                loadChildren: () => import('./admin/admin.module').then((module) => module.RdioScannerAdminPageModule),
                path: 'admin',
            },
        ],
    },
];

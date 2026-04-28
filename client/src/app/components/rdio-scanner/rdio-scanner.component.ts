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

import { AfterViewInit, Component, ElementRef, HostListener, OnDestroy, OnInit, ViewChild } from '@angular/core';
import { MatSidenav } from '@angular/material/sidenav';
import { MatSnackBar } from '@angular/material/snack-bar';
import { timer } from 'rxjs';
import { RdioScannerEvent, RdioScannerLivefeedMode } from './rdio-scanner';
import { RdioScannerService } from './rdio-scanner.service';
import { RdioScannerNativeComponent } from './native/native.component';
import { RdioScannerPublicStatsComponent } from './stats/public-stats.component';
import { RdioScannerSearchComponent } from './search/search.component';

@Component({
    selector: 'rdio-scanner',
    styleUrls: ['./rdio-scanner.component.scss'],
    templateUrl: './rdio-scanner.component.html',
})
export class RdioScannerComponent implements AfterViewInit, OnDestroy, OnInit {
    private eventSubscription = this.rdioScannerService.event.subscribe((event: RdioScannerEvent) => this.eventHandler(event));

    private livefeedMode: RdioScannerLivefeedMode = RdioScannerLivefeedMode.Offline;

    @ViewChild('searchPanel') private searchPanel: MatSidenav | undefined;

    @ViewChild('selectPanel') private selectPanel: MatSidenav | undefined;

    @ViewChild('statsPanel') private statsPanel: MatSidenav | undefined;

    @ViewChild('statsComponent') public statsComponent: RdioScannerPublicStatsComponent | undefined;

    @ViewChild('searchComponent') private searchComponent: RdioScannerSearchComponent | undefined;

    @ViewChild('scrollableSearch') private scrollableSearch: ElementRef | undefined;

    constructor(
        private matSnackBar: MatSnackBar,
        private ngElementRef: ElementRef,
        private rdioScannerService: RdioScannerService,
    ) { }

    @HostListener('window:beforeunload', ['$event'])
    exitNotification(event: BeforeUnloadEvent): void {
        if (this.livefeedMode !== RdioScannerLivefeedMode.Offline) {
            event.preventDefault();

            event.returnValue = 'Live Feed is ON, do you really want to leave?';
        }
    }

    ngAfterViewInit(): void {
        // The deepLinkCall event can fire before this component subscribes —
        // the index.html early-WS can deliver CFG to the service before
        // Angular has even constructed this component. In that case the
        // event handler below never sees the emit. Pull any pending
        // deep-link ID directly so the share-link flow still runs.
        setTimeout(() => {
            const id = this.rdioScannerService.consumePendingDeepLink();
            if (id) {
                this.searchPanel?.open();
                setTimeout(() => this.searchComponent?.focusCall(id), 0);
            }
        }, 300);
    }

    ngOnDestroy(): void {
        this.eventSubscription.unsubscribe();
    }

    ngOnInit(): void {
        /*
         * BEGIN OF RED TAPE:
         * 
         * By modifying, deleting or disabling the following lines, you harm
         * the open source project and its author.  Rdio Scanner represents a lot of
         * investment in time, support, testing and hardware.
         * 
         * Be respectful, sponsor the project if you can, use native apps when possible.
         * 
         */
        timer(10000).subscribe(() => {
            const ua: string = navigator.userAgent;

            if (ua.includes('Android') || ua.includes('iPad') || ua.includes('iPhone')) {
                this.matSnackBar.openFromComponent(RdioScannerNativeComponent, { panelClass: 'snackbar-white' });
            }
        });
        /**
         * END OF RED TAPE.
         */
    }

    scrollTop(e: HTMLElement): void {
        setTimeout(() => e.scrollTo(0, 0));
    }

    start(): void {
        this.rdioScannerService.startLivefeed();
    }

    stop(): void {
        this.rdioScannerService.stopLivefeed();

        this.searchPanel?.close();
        this.selectPanel?.close();
        this.statsPanel?.close();
    }

    toggleFullscreen(): void {
        if (document.fullscreenElement) {
            const el: {
                exitFullscreen?: () => void;
                mozCancelFullScreen?: () => void;
                msExitFullscreen?: () => void;
                webkitExitFullscreen?: () => void;
            } = document;

            if (el.exitFullscreen) {
                el.exitFullscreen();

            } else if (el.mozCancelFullScreen) {
                el.mozCancelFullScreen();

            } else if (el.msExitFullscreen) {
                el.msExitFullscreen();

            } else if (el.webkitExitFullscreen) {
                el.webkitExitFullscreen();
            }

        } else {
            const el = this.ngElementRef.nativeElement;

            if (el.requestFullscreen) {
                el.requestFullscreen();

            } else if (el.mozRequestFullScreen) {
                el.mozRequestFullScreen();

            } else if (el.msRequestFullscreen) {
                el.msRequestFullscreen();

            } else if (el.webkitRequestFullscreen) {
                el.webkitRequestFullscreen();
            }
        }
    }

    onSearchFocusedCall(scrollable: HTMLElement): void {
        // Called when the search component has highlighted the target call
        // and wants the sidenav content scrolled to it. The scrollTop(0)
        // trick resets the scroll, then the component scrolls the row into
        // view itself via scrollIntoView.
        scrollable?.scrollTo?.(0, 0);
    }

    private eventHandler(event: RdioScannerEvent): void {
        if (event.livefeedMode) {
            this.livefeedMode = event.livefeedMode;
        }

        if (typeof event.deepLinkCall === 'number' && event.deepLinkCall > 0) {
            // Route the deep-link: open the search panel, then ask the search
            // component to locate/highlight/play the call. Mark it consumed
            // so the ngAfterViewInit fallback doesn't also run it.
            const id = this.rdioScannerService.consumePendingDeepLink() ?? event.deepLinkCall;
            this.searchPanel?.open();
            setTimeout(() => this.searchComponent?.focusCall(id), 0);
        }
    }
}

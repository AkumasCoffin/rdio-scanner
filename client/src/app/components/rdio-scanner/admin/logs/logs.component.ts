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

import { Component, OnDestroy, ViewChild } from '@angular/core';
import { FormBuilder } from '@angular/forms';
import { MatPaginator } from '@angular/material/paginator';
import { BehaviorSubject, Subscription } from 'rxjs';
import { debounceTime } from 'rxjs/operators';
import { Log, LogsQuery, LogsQueryOptions, RdioScannerAdminService } from '../admin.service';

@Component({
    selector: 'rdio-scanner-admin-logs',
    styleUrls: ['./logs.component.scss'],
    templateUrl: './logs.component.html',
})
export class RdioScannerAdminLogsComponent implements OnDestroy {
    form = this.ngFormBuilder.group({
        category: [null],
        date: [null],
        level: [null],
        search: [null],
        sort: [-1],
    });

    // Friendly log categories surfaced in the filter dropdown. The key is sent
    // to the server, which maps it to a set of message LIKE patterns (see
    // logCategoryPatterns in server/log.go). Keep the two lists in sync.
    readonly categories = [
        { value: 'connections', label: 'Listener connections' },
        { value: 'access', label: 'Access & login' },
        { value: 'transcription', label: 'Transcription' },
        { value: 'sharelink', label: 'Share-link requests' },
        { value: 'config', label: 'Configuration' },
        { value: 'lifecycle', label: 'Server lifecycle' },
    ];

    logs = new BehaviorSubject(new Array<Log | null>(10));

    logsQuery: LogsQuery | undefined = undefined;

    logsQueryPending = false;

    private limit = 200;

    private offset = 0;

    private searchSubscription: Subscription;

    @ViewChild(MatPaginator) private paginator: MatPaginator | undefined;

    constructor(private adminService: RdioScannerAdminService, private ngFormBuilder: FormBuilder) {
        // Debounce free-text message search so we don't fire a query on every
        // keystroke. Other controls trigger formHandler() directly from the
        // template since they change one value at a time.
        this.searchSubscription = this.form.controls['search'].valueChanges
            .pipe(debounceTime(400))
            .subscribe(() => {
                if (!this.logsQueryPending) {
                    this.formHandler();
                }
            });
    }

    ngOnDestroy(): void {
        this.searchSubscription.unsubscribe();
    }

    formHandler(): void {
        this.paginator?.firstPage();

        this.reload();
    }

    reset(): void {
        // emitEvent: false so clearing the search field doesn't also trigger
        // the debounced valueChanges handler on top of the formHandler below.
        this.form.reset({
            category: null,
            date: null,
            level: null,
            search: null,
            sort: -1,
        }, { emitEvent: false });

        this.formHandler();
    }

    refresh(): void {
        if (!this.paginator) {
            return;
        }

        const from = this.paginator.pageIndex * this.paginator.pageSize;

        const to = this.paginator.pageIndex * this.paginator.pageSize + this.paginator.pageSize - 1;

        if (!this.logsQueryPending && (from >= this.offset + this.limit || from < this.offset)) {
            this.reload();

        } else if (this.logsQuery) {
            const logs: Array<Log | null> = this.logsQuery.logs.slice(from % this.limit, to % this.limit + 1);

            while (logs.length < this.logs.value.length) {
                logs.push(null);
            }

            this.logs.next(logs);
        }
    }

    async reload(): Promise<void> {
        const pageIndex = this.paginator?.pageIndex || 0;

        const pageSize = this.paginator?.pageSize || 0;

        this.offset = Math.floor((pageIndex * pageSize) / this.limit) * this.limit;

        const options: LogsQueryOptions = {
            limit: this.limit,
            offset: this.offset,
            sort: this.form.value.sort,
        };

        if (typeof this.form.value.level === 'string') {
            options.level = this.form.value.level;
        }

        if (typeof this.form.value.category === 'string') {
            options.category = this.form.value.category;
        }

        if (typeof this.form.value.search === 'string' && this.form.value.search.trim() !== '') {
            options.search = this.form.value.search.trim();
        }

        if (typeof this.form.value.date === 'string') {
            options.date = new Date(Date.parse(this.form.value.date));
        }

        this.logsQueryPending = true;

        // emitEvent: false so toggling the disabled state during the request
        // doesn't fire the search valueChanges subscription (which would loop).
        this.form.disable({ emitEvent: false });

        this.logsQuery = await this.adminService.getLogs(options);

        this.form.enable({ emitEvent: false });

        this.logsQueryPending = false;

        this.refresh();
    }
}

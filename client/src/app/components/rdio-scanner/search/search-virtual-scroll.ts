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

import { CdkVirtualScrollViewport, VIRTUAL_SCROLL_STRATEGY, VirtualScrollStrategy } from '@angular/cdk/scrolling';
import { Directive, Input, OnChanges, forwardRef } from '@angular/core';
import { Observable, Subject } from 'rxjs';
import { distinctUntilChanged } from 'rxjs/operators';

/**
 * A virtual scroll strategy for rows whose height is not known in advance.
 *
 * The CDK ships a fixed-size strategy and nothing else — its autosize strategy
 * lives in `@angular/cdk-experimental`, which this project does not depend on.
 * Search rows carry a talkgroup name that wraps, a transcript of any length,
 * and whatever a plugin puts in the row slot, so a single `itemSize` would
 * misplace every row below the first one that disagreed with it.
 *
 * So heights are measured instead. Every row starts at an estimate, is measured
 * the first time it renders, and the running average of what has been measured
 * becomes the estimate for everything still unseen — which keeps the scrollbar
 * from lurching as the user pages into a long list.
 */
export class RdioScannerMeasuredScrollStrategy implements VirtualScrollStrategy {
    private indexChange = new Subject<number>();

    scrolledIndexChange: Observable<number> = this.indexChange.pipe(distinctUntilChanged());

    private viewport: CdkVirtualScrollViewport | undefined;

    /** Height of each row. Estimated until the row has been rendered once. */
    private sizes: number[] = [];

    /** True once `sizes[i]` came from the DOM rather than from the estimate. */
    private measured: boolean[] = [];

    private measuredCount = 0;
    private measuredTotal = 0;

    /** Prefix sums: `tops[i]` is the top edge of row i, `tops[length]` the total. */
    private tops: number[] = [0];

    /**
     * Width the current measurements were taken at. Row height is a function of
     * width — a wrapped transcript is the whole reason this strategy exists —
     * so every measurement is stale the moment the viewport is resized.
     */
    private measuredAtWidth = 0;

    /** Re-entrancy guard: measuring can change the range, which renders again. */
    private measuring = false;

    constructor(private estimate: number, private buffer: number) { }

    /**
     * Re-reads the directive's inputs. The strategy instance itself is never
     * replaced — the viewport injected it once, at construction — so the knobs
     * are mutated in place the way the CDK's own fixed-size strategy does.
     */
    configure(estimate: number, buffer: number): void {
        this.estimate = estimate;
        this.buffer = buffer;

        this.onDataLengthChanged();
    }

    attach(viewport: CdkVirtualScrollViewport): void {
        this.viewport = viewport;

        this.onDataLengthChanged();
    }

    detach(): void {
        this.indexChange.complete();

        this.viewport = undefined;
    }

    onContentScrolled(): void {
        this.updateRenderedRange();
    }

    onDataLengthChanged(): void {
        if (!this.viewport) {
            return;
        }

        const width = this.viewport.elementRef.nativeElement.clientWidth;

        if (width !== this.measuredAtWidth) {
            // Everything measured at another width is a guess now. Dropping it
            // wholesale is cheaper and more honest than keeping heights that
            // will be wrong for every row that wraps differently.
            this.measuredAtWidth = width;
            this.measured = [];
            this.measuredCount = 0;
            this.measuredTotal = 0;
        }

        const length = this.viewport.getDataLength();
        const fallback = this.averageSize();

        // Rows are only ever appended, so anything already measured keeps its
        // height and only the new tail needs seeding.
        for (let i = 0; i < length; i++) {
            if (!this.measured[i]) {
                this.sizes[i] = fallback;
            }
        }

        this.sizes.length = length;
        this.measured.length = length;

        this.rebuildOffsets();
        this.updateRenderedRange();
    }

    onContentRendered(): void {
        this.measureRendered();
    }

    onRenderedOffsetChanged(): void {
        // The offset is only ever set from here, so there is nothing to react to.
    }

    scrollToIndex(index: number, behavior: ScrollBehavior): void {
        if (!this.viewport) {
            return;
        }

        const clamped = Math.max(0, Math.min(index, this.sizes.length - 1));

        this.viewport.scrollToOffset(this.tops[clamped] || 0, behavior);
    }

    /** Height to assume for a row nobody has seen yet. */
    private averageSize(): number {
        return this.measuredCount > 0 ? this.measuredTotal / this.measuredCount : this.estimate;
    }

    private rebuildOffsets(): void {
        const length = this.sizes.length;

        this.tops = new Array(length + 1);
        this.tops[0] = 0;

        for (let i = 0; i < length; i++) {
            this.tops[i + 1] = this.tops[i] + (this.sizes[i] || this.estimate);
        }

        this.viewport?.setTotalContentSize(this.tops[length]);
    }

    /** Largest index whose top edge is at or above `offset`. */
    private indexAt(offset: number): number {
        const length = this.sizes.length;

        if (length === 0) {
            return 0;
        }

        let low = 0;
        let high = length - 1;

        while (low < high) {
            const mid = (low + high + 1) >> 1;

            if (this.tops[mid] <= offset) {
                low = mid;
            } else {
                high = mid - 1;
            }
        }

        return low;
    }

    private updateRenderedRange(): void {
        const viewport = this.viewport;

        if (!viewport) {
            return;
        }

        const length = this.sizes.length;

        if (length === 0) {
            viewport.setRenderedRange({ start: 0, end: 0 });
            viewport.setRenderedContentOffset(0);
            viewport.setTotalContentSize(0);

            return;
        }

        const viewportSize = viewport.getViewportSize();
        const scrollOffset = Math.max(0, viewport.measureScrollOffset());

        const start = this.indexAt(scrollOffset - this.buffer);
        const end = Math.min(length, this.indexAt(scrollOffset + viewportSize + this.buffer) + 1);

        viewport.setRenderedRange({ start, end });
        viewport.setRenderedContentOffset(this.tops[start]);

        this.indexChange.next(this.indexAt(scrollOffset));
    }

    private measureRendered(): void {
        const viewport = this.viewport;

        if (!viewport || this.measuring) {
            return;
        }

        const wrapper = viewport.elementRef.nativeElement
            .querySelector('.cdk-virtual-scroll-content-wrapper') as HTMLElement | null;

        if (!wrapper) {
            return;
        }

        const range = viewport.getRenderedRange();
        const children = wrapper.children;

        if (!children.length) {
            return;
        }

        // Measured as the distance between one row's top and the next row's,
        // not as the row's own height: that distance includes whatever margin
        // or gap the stylesheet puts between rows, which `offsetHeight` does
        // not. Getting it wrong by a few pixels a row compounds into a
        // scrollbar that no longer matches the content.
        const rects: DOMRect[] = [];

        for (let i = 0; i < children.length; i++) {
            rects.push((children[i] as HTMLElement).getBoundingClientRect());
        }

        let gap = 0;

        for (let i = 1; i < rects.length; i++) {
            gap = Math.max(gap, rects[i].top - rects[i - 1].bottom);
        }

        let changed = false;

        for (let i = 0; i < rects.length; i++) {
            const index = range.start + i;

            if (index >= this.sizes.length) {
                break;
            }

            const size = (i + 1 < rects.length ? rects[i + 1].top - rects[i].top : rects[i].height + gap);

            if (size <= 0) {
                continue;
            }

            if (this.measured[index]) {
                if (Math.abs(size - this.sizes[index]) < 0.5) {
                    continue;
                }

                this.measuredTotal -= this.sizes[index];
                this.measuredCount -= 1;
            }

            this.sizes[index] = size;
            this.measured[index] = true;
            this.measuredTotal += size;
            this.measuredCount += 1;

            changed = true;
        }

        if (!changed) {
            return;
        }

        // Re-running the range with the corrected heights can render different
        // rows, which lands back in here. The guard keeps that to one pass —
        // heights converge immediately, since a row measured at this width does
        // not change when it is measured again.
        this.measuring = true;

        try {
            this.rebuildOffsets();
            this.updateRenderedRange();
        } finally {
            this.measuring = false;
        }
    }
}

/**
 * Exported rather than inlined in the provider because a factory referenced by
 * a decorator has to be statically resolvable.
 */
export function rdioMeasuredScrollStrategyFactory(
    directive: RdioScannerMeasuredItemsDirective,
): RdioScannerMeasuredScrollStrategy {
    return directive.scrollStrategy;
}

/**
 * Attaches {@link RdioScannerMeasuredScrollStrategy} to a
 * `cdk-virtual-scroll-viewport`, in place of the `itemSize` input that selects
 * the CDK's fixed-size strategy.
 */
@Directive({
    selector: 'cdk-virtual-scroll-viewport[rdioMeasuredItems]',
    providers: [{
        provide: VIRTUAL_SCROLL_STRATEGY,
        useFactory: rdioMeasuredScrollStrategyFactory,
        deps: [forwardRef(() => RdioScannerMeasuredItemsDirective)],
    }],
})
export class RdioScannerMeasuredItemsDirective implements OnChanges {
    /** Height to assume for rows that have never been rendered. */
    @Input() rdioMeasuredItems: number | string = 96;

    /** Pixels of extra content to keep rendered above and below the viewport. */
    @Input() rdioMeasuredBuffer: number | string = 600;

    readonly scrollStrategy = new RdioScannerMeasuredScrollStrategy(96, 600);

    ngOnChanges(): void {
        this.scrollStrategy.configure(
            Number(this.rdioMeasuredItems) || 96,
            Number(this.rdioMeasuredBuffer) || 600,
        );
    }
}

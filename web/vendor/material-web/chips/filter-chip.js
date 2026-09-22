/**
 * @license
 * Copyright 2023 Google LLC
 * SPDX-License-Identifier: Apache-2.0
 */
import { __decorate } from "tslib";
import { customElement } from 'lit/decorators.js';
import { styles as elevatedStyles } from './internal/elevated-styles.cssresult.js';
import { FilterChip } from './internal/filter-chip.js';
import { styles } from './internal/filter-styles.cssresult.js';
import { styles as selectableStyles } from './internal/selectable-styles.cssresult.js';
import { styles as sharedStyles } from './internal/shared-styles.cssresult.js';
import { styles as trailingIconStyles } from './internal/trailing-icon-styles.cssresult.js';
/**
 * TODO(b/243982145): add docs
 *
 * @final
 * @suppress {visibility}
 */
let MdFilterChip = class MdFilterChip extends FilterChip {
};
MdFilterChip.styles = [
    sharedStyles,
    elevatedStyles,
    trailingIconStyles,
    selectableStyles,
    styles,
];
MdFilterChip = __decorate([
    customElement('md-filter-chip')
], MdFilterChip);
export { MdFilterChip };
//# sourceMappingURL=filter-chip.js.map
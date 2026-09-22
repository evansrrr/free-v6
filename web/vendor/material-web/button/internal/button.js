/**
 * @license
 * Copyright 2019 Google LLC
 * SPDX-License-Identifier: Apache-2.0
 */
import { __decorate } from "tslib";
import '../../focus/md-focus-ring.js';
import '../../ripple/ripple.js';
import { html, isServer, LitElement, nothing } from 'lit';
import { property, query, queryAssignedElements } from 'lit/decorators.js';
import { mixinDelegatesAria } from '../../internal/aria/delegate.js';
import { dispatchActivationClick, isActivationClick, } from '../../internal/events/form-label-activation.js';
import { mixinElementInternals } from '../../labs/behaviors/element-internals.js';
import { mixinFormAssociated } from '../../labs/behaviors/form-associated.js';
import { mixinFormSubmitter } from '../../labs/behaviors/form-submitter.js';
// Separate variable needed for closure.
const buttonBaseClass = mixinDelegatesAria(mixinFormSubmitter(mixinFormAssociated(mixinElementInternals(LitElement))));
/**
 * A button component.
 */
export class Button extends buttonBaseClass {
    constructor() {
        super();
        /**
         * Whether or not the button is "soft-disabled" (disabled but still
         * focusable).
         *
         * Use this when a button needs increased visibility when disabled. See
         * https://www.w3.org/WAI/ARIA/apg/practices/keyboard-interface/#kbd_disabled_controls
         * for more guidance on when this is needed.
         */
        this.softDisabled = false;
        /**
         * The URL that the link button points to.
         */
        this.href = '';
        /**
         * The filename to use when downloading the linked resource.
         * If not specified, the browser will determine a filename.
         * This is only applicable when the button is used as a link (`href` is set).
         */
        this.download = '';
        /**
         * Where to display the linked `href` URL for a link button. Common options
         * include `_blank` to open in a new tab.
         */
        this.target = '';
        /**
         * Whether to render the icon at the inline end of the label rather than the
         * inline start.
         *
         * _Note:_ Link buttons cannot have trailing icons.
         */
        this.trailingIcon = false;
        /**
         * Whether to display the icon or not.
         */
        this.hasIcon = false;
        if (!isServer) {
            this.addEventListener('click', this.handleClick.bind(this));
        }
    }
    focus() {
        this.buttonElement?.focus();
    }
    blur() {
        this.buttonElement?.blur();
    }
    render() {
        const isRippleDisabled = this.disabled || this.softDisabled;
        const buttonOrLink = this.href ? this.renderLink() : this.renderButton();
        // TODO(b/310046938): due to a limitation in focus ring/ripple, we can't use
        // the same ID for different elements, so we change the ID instead.
        const buttonId = this.href ? 'link' : 'button';
        return html `
      ${this.renderElevationOrOutline?.()}
      <div class="background"></div>
      <md-focus-ring part="focus-ring" for=${buttonId}></md-focus-ring>
      <md-ripple
        part="ripple"
        for=${buttonId}
        ?disabled="${isRippleDisabled}"></md-ripple>
      ${buttonOrLink}
    `;
    }
    renderButton() {
        // Needed for closure conformance
        const { ariaLabel, ariaHasPopup, ariaExpanded } = this;
        return html `<button
      id="button"
      class="button"
      ?disabled=${this.disabled}
      aria-disabled=${this.softDisabled || nothing}
      aria-label="${ariaLabel || nothing}"
      aria-haspopup="${ariaHasPopup || nothing}"
      aria-expanded="${ariaExpanded || nothing}">
      ${this.renderContent()}
    </button>`;
    }
    renderLink() {
        // Needed for closure conformance
        const { ariaLabel, ariaHasPopup, ariaExpanded } = this;
        return html `<a
      id="link"
      class="button"
      aria-label="${ariaLabel || nothing}"
      aria-haspopup="${ariaHasPopup || nothing}"
      aria-expanded="${ariaExpanded || nothing}"
      aria-disabled=${this.disabled || this.softDisabled || nothing}
      tabindex="${this.disabled && !this.softDisabled ? -1 : nothing}"
      href=${this.href}
      download=${this.download || nothing}
      target=${this.target || nothing}
      >${this.renderContent()}
    </a>`;
    }
    renderContent() {
        const icon = html `<slot
      name="icon"
      @slotchange="${this.handleSlotChange}"></slot>`;
        return html `
      <span class="touch"></span>
      ${this.trailingIcon ? nothing : icon}
      <span class="label"><slot></slot></span>
      ${this.trailingIcon ? icon : nothing}
    `;
    }
    handleClick(event) {
        // If the button is soft-disabled or a disabled link, we need to explicitly
        // prevent the click from propagating to other event listeners as well as
        // prevent the default action.
        if (this.softDisabled || (this.disabled && this.href)) {
            event.stopImmediatePropagation();
            event.preventDefault();
            return;
        }
        if (!isActivationClick(event) || !this.buttonElement) {
            return;
        }
        this.focus();
        dispatchActivationClick(this.buttonElement);
    }
    handleSlotChange() {
        this.hasIcon = this.assignedIcons.length > 0;
    }
}
/** @nocollapse */
Button.shadowRootOptions = {
    mode: 'open',
    delegatesFocus: true,
};
__decorate([
    property({ type: Boolean, attribute: 'soft-disabled', reflect: true })
], Button.prototype, "softDisabled", void 0);
__decorate([
    property()
], Button.prototype, "href", void 0);
__decorate([
    property()
], Button.prototype, "download", void 0);
__decorate([
    property()
], Button.prototype, "target", void 0);
__decorate([
    property({ type: Boolean, attribute: 'trailing-icon', reflect: true })
], Button.prototype, "trailingIcon", void 0);
__decorate([
    property({ type: Boolean, attribute: 'has-icon', reflect: true })
], Button.prototype, "hasIcon", void 0);
__decorate([
    query('.button')
], Button.prototype, "buttonElement", void 0);
__decorate([
    queryAssignedElements({ slot: 'icon', flatten: true })
], Button.prototype, "assignedIcons", void 0);
//# sourceMappingURL=button.js.map
/**
 * @license
 * Copyright 2023 Google LLC
 * SPDX-License-Identifier: Apache-2.0
 */
import { __decorate } from "tslib";
import { isServer } from 'lit';
import { property } from 'lit/decorators.js';
import { afterDispatch, setupDispatchHooks, } from '../../internal/events/dispatch-hooks.js';
import { internals } from './element-internals.js';
/**
 * Mixes in form submitter behavior for a class.
 *
 * A click listener is added to each element instance. If the click is not
 * default prevented, it will submit the element's form, if any.
 *
 * @example
 * ```ts
 * const base = mixinFormSubmitter(mixinElementInternals(LitElement));
 * class MyButton extends base {
 *   static formAssociated = true;
 * }
 * ```
 *
 * @param base The class to mix functionality into.
 * @return The provided class with `FormSubmitter` mixed in.
 */
export function mixinFormSubmitter(base) {
    class FormSubmitterElement extends base {
        // Name attribute must reflect synchronously for form integration.
        get name() {
            return this.getAttribute('name') ?? '';
        }
        set name(name) {
            this.setAttribute('name', name);
        }
        // Mixins must have a constructor with `...args: any[]`
        // tslint:disable-next-line:no-any
        constructor(...args) {
            super(...args);
            this.type = 'submit';
            this.value = '';
            if (isServer)
                return;
            setupDispatchHooks(this, 'click');
            this.addEventListener('click', async (event) => {
                const isReset = this.type === 'reset';
                const isSubmit = this.type === 'submit';
                const elementInternals = this[internals];
                const { form } = elementInternals;
                if (!form || !(isSubmit || isReset)) {
                    return;
                }
                afterDispatch(event, () => {
                    if (event.defaultPrevented) {
                        return;
                    }
                    if (isReset) {
                        form.reset();
                        return;
                    }
                    // form.requestSubmit(submitter) does not work with form associated custom
                    // elements. This patches the dispatched submit event to add the correct
                    // `submitter`.
                    // See https://github.com/WICG/webcomponents/issues/814
                    form.addEventListener('submit', (submitEvent) => {
                        Object.defineProperty(submitEvent, 'submitter', {
                            configurable: true,
                            enumerable: true,
                            get: () => this,
                        });
                    }, { capture: true, once: true });
                    elementInternals.setFormValue(this.value);
                    form.requestSubmit();
                });
            });
        }
    }
    __decorate([
        property()
    ], FormSubmitterElement.prototype, "type", void 0);
    __decorate([
        property({ reflect: true })
    ], FormSubmitterElement.prototype, "value", void 0);
    return FormSubmitterElement;
}
//# sourceMappingURL=form-submitter.js.map
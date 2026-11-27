// Settings-only components (select, chips, text fields). Loaded via a dynamic
// import once the first frame has painted, so they do not join the initial
// upgrade batch. See preloadSettingsComponents() in app.js.
import '@material/web/chips/filter-chip.js';
import '@material/web/select/outlined-select.js';
import '@material/web/select/select-option.js';
import '@material/web/textfield/outlined-text-field.js';

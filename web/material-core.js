// First-screen components only. Everything the dashboard, dialogs and titlebar
// need before the user interacts. Settings-only components load separately in
// material-settings.js so the initial upgrade batch (the ~57ms long task) is
// split across two idle frames.
import '@material/web/button/filled-button.js';
import '@material/web/button/filled-tonal-button.js';
import '@material/web/button/outlined-button.js';
import '@material/web/button/text-button.js';
import '@material/web/divider/divider.js';
import '@material/web/icon/icon.js';
import '@material/web/iconbutton/icon-button.js';
import '@material/web/progress/circular-progress.js';

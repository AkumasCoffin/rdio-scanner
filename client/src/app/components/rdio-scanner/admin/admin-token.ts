/*
 * Where the admin session token lives.
 *
 * Both the admin panel and the scanner need it — the panel to talk to
 * /api/admin/*, the scanner to decide whether to offer admin-only controls and
 * to call the plugin routes behind them. It used to be read straight out of
 * sessionStorage in two places, each with its own copy of the key string.
 *
 * sessionStorage is per tab, which made admin controls in the scanner appear
 * only when you had logged in and then navigated to the scanner *in that same
 * tab*. Opening the scanner in a second tab, or from a bookmark, silently hid
 * them: same browser, same person, same live session, no button. localStorage
 * is shared across tabs of the origin, so the session follows the person rather
 * than the tab.
 *
 * The server, not the browser, remains the authority on what a token is worth.
 * It keeps issued tokens in an in-memory allowlist of five (server admin.go),
 * so every restart invalidates all of them, a sixth login evicts the oldest,
 * and logout drops the one it was given. A token that outlives its tab is
 * therefore still short-lived in practice, and it grants nothing on its own —
 * every admin route validates it against that list.
 */

const ADMIN_TOKEN_KEY = 'rdio-scanner-admin-token';

/**
 * Reads the current admin token, or '' when there is no session.
 *
 * sessionStorage is still consulted so that a tab which logged in before this
 * changed keeps working until it is closed, rather than being signed out by an
 * update it did not ask for.
 */
export function readAdminToken(): string {
    return window?.localStorage?.getItem(ADMIN_TOKEN_KEY)
        || window?.sessionStorage?.getItem(ADMIN_TOKEN_KEY)
        || '';
}

export function writeAdminToken(token: string): void {
    if (!token) {
        clearAdminToken();
        return;
    }

    window?.localStorage?.setItem(ADMIN_TOKEN_KEY, token);
}

/** Clears the session everywhere it could be, including the older location. */
export function clearAdminToken(): void {
    window?.localStorage?.removeItem(ADMIN_TOKEN_KEY);
    window?.sessionStorage?.removeItem(ADMIN_TOKEN_KEY);
}

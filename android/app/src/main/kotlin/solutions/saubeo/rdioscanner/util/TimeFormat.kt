package solutions.saubeo.rdioscanner.util

import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * Parse an ISO-8601 / RFC3339 instant emitted by the server's call
 * timestamps. The server formats via Go's `time.RFC3339` after a
 * `.UTC()` call, so the wire form is always `yyyy-MM-ddTHH:mm:ssZ`
 * — but other clients / older payloads may send offset-suffixed
 * (`+10:00`) or millisecond-precision variants, so we accept all.
 *
 * Crucially, every fallback explicitly tags its timezone. The previous
 * implementation had a `'Z'` literal-character pattern with no
 * `timeZone` override, so when the `XXX` pattern silently failed
 * the literal-Z parser interpreted the timestamp components in the
 * JVM's *default* zone — every call time ended up offset by the
 * viewer's UTC delta.
 */
fun parseIsoInstant(iso: String): Date? {
    val utc = TimeZone.getTimeZone("UTC")
    val patterns = arrayOf(
        // ISO 8601 offset (`Z` or `+HH:MM`). Java's `X` family parses
        // both correctly without an explicit timeZone override.
        "yyyy-MM-dd'T'HH:mm:ssXXX",
        "yyyy-MM-dd'T'HH:mm:ss.SSSXXX",
        "yyyy-MM-dd'T'HH:mm:ss.SSSSSSXXX",
        // Literal `Z` fallback — explicit UTC timezone so the parsed
        // instant matches the wire value.
        "yyyy-MM-dd'T'HH:mm:ss'Z'",
        "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'",
        "yyyy-MM-dd'T'HH:mm:ss.SSSSSS'Z'",
    )
    for (pattern in patterns) {
        runCatching {
            val fmt = SimpleDateFormat(pattern, Locale.US)
            if (pattern.endsWith("'Z'")) fmt.timeZone = utc
            val result = fmt.parse(iso)
            if (result != null) return result
        }
    }
    return null
}

package solutions.saubeo.rdioscanner.util

import java.time.Instant
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Date

/**
 * Parse an ISO-8601 / RFC3339 instant emitted by the server's call
 * timestamps. The server formats via Go's `time.RFC3339` (and sometimes
 * `time.RFC3339Nano`), so the wire form can be any of:
 *
 *   2026-05-18T19:23:45Z
 *   2026-05-18T19:23:45.123Z
 *   2026-05-18T19:23:45.123456789Z
 *   2026-05-18T15:23:45-04:00
 *
 * We use `java.time` (minSdk 26 ⇒ available natively) which understands
 * all of these without pattern juggling. The previous SimpleDateFormat
 * approach silently fell through to a literal-`Z` pattern with no
 * explicit zone, so timestamps were reinterpreted in the JVM default
 * zone and ended up offset by the viewer's UTC delta.
 */
fun parseIsoInstant(iso: String): Date? {
    runCatching {
        return Date.from(OffsetDateTime.parse(iso, DateTimeFormatter.ISO_OFFSET_DATE_TIME).toInstant())
    }
    runCatching {
        return Date.from(Instant.parse(iso))
    }
    return null
}

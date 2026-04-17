package solutions.saubeo.rdioscanner.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

/** Palette mirrored from client/src/styles.scss. */
object RdioPalette {
    val Bg = Color(0xFF020617)
    val BgGradientTop = Color(0xFF111827)
    val BgElevated = Color(0xFF10131B)
    val BgElevatedSoft = Color(0xFF151925)
    val Surface = Color(0xE60F172A)          // rgba(15, 23, 42, 0.9)
    val SurfaceDim = Color(0x801E293B)       // rgba(30, 41, 59, 0.5)
    val BorderSubtle = Color(0x5994A3B8)     // rgba(148, 163, 184, 0.35)
    val BorderSubtleSoft = Color(0x2694A3B8) // rgba(148, 163, 184, 0.15)
    val Accent = Color(0xFFF97316)
    val AccentSoft = Color(0x2EF97316)

    val TextMain = Color(0xFFF9FAFB)
    val TextMuted = Color(0xFFA1A5B6)
    val TextSoft = Color(0xFF6B7280)

    val Green = Color(0xFF22C55E)
    val GreenSoft = Color(0x3322C55E)
    val Red = Color(0xFFEF4444)
    val RedSoft = Color(0x33EF4444)
    val Yellow = Color(0xFFEAB308)
    val YellowSoft = Color(0x33EAB308)
    val Blue = Color(0xFF3B82F6)
    val Cyan = Color(0xFF06B6D4)
    val Magenta = Color(0xFFA855F7)
    val White = Color(0xFFF9FAFB)
}

object RdioShape {
    val MD = 12.dp
    val LG = 18.dp
}

/** Surface-ish colors; real visual styling lives in the scanner composables. */
private val Scheme = darkColorScheme(
    primary = RdioPalette.Accent,
    onPrimary = Color.White,
    secondary = RdioPalette.Accent,
    onSecondary = Color.White,
    background = RdioPalette.Bg,
    onBackground = RdioPalette.TextMain,
    surface = RdioPalette.BgElevated,
    onSurface = RdioPalette.TextMain,
    surfaceVariant = RdioPalette.BgElevatedSoft,
    onSurfaceVariant = RdioPalette.TextMuted,
    outline = RdioPalette.BorderSubtle,
    error = RdioPalette.Red,
)

val LocalRdioPalette = staticCompositionLocalOf { RdioPalette }

@Composable
fun RdioTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = Scheme, content = content)
}

/** Returns the hex color for a webapp-style `led: "blue" | "cyan" | ...` string. */
fun ledColor(name: String?): Color = when (name?.lowercase()) {
    "blue" -> RdioPalette.Blue
    "cyan" -> RdioPalette.Cyan
    "green" -> RdioPalette.Green
    "magenta" -> RdioPalette.Magenta
    "orange" -> RdioPalette.Accent
    "red" -> RdioPalette.Red
    "white" -> RdioPalette.White
    "yellow" -> RdioPalette.Yellow
    else -> RdioPalette.Green
}

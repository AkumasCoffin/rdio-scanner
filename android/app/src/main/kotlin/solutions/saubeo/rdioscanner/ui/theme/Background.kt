package solutions.saubeo.rdioscanner.ui.theme

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush

/**
 * Radial gradient background used behind every screen, matching the webapp.
 *
 * The outer Box draws the gradient edge-to-edge (behind the status bar and
 * gesture pill) so the dark navy carries right to the screen edges. The
 * inner Box applies [WindowInsets.safeDrawing] as padding so any actual
 * content — top bar, scanner LCD, button grid — sits inside the safe area
 * and never collides with the status bar, display cutouts, or the gesture
 * navigation handle on Android 15's edge-to-edge layout.
 */
@Composable
fun RdioBackground(content: @Composable () -> Unit) {
    val base = Brush.radialGradient(
        0.0f to RdioPalette.BgGradientTop,
        0.52f to RdioPalette.Bg,
        1.0f to RdioPalette.Bg,
        center = Offset(0f, 0f),
        radius = 2200f,
    )
    Box(Modifier.fillMaxSize().background(RdioPalette.Bg).background(base)) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .windowInsetsPadding(WindowInsets.safeDrawing),
        ) {
            content()
        }
    }
}

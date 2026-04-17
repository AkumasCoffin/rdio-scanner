package solutions.saubeo.rdioscanner.ui.theme

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush

/** The radial gradient background used behind every screen in the webapp. */
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
        content()
    }
}

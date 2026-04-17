package solutions.saubeo.rdioscanner.ui.components

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import solutions.saubeo.rdioscanner.ui.LocalClickSound
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette

enum class RdioButtonState { Default, Off, On, Partial }
enum class RdioClickTone { Click, Activate, Deactivate, Denied }

/**
 * Matches the webapp `.rdio-button` in common.scss: uppercase label,
 * subtle surface with colored LED dot + radial tint for on/off/partial.
 */
@Composable
fun RdioButton(
    label: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    state: RdioButtonState = RdioButtonState.Default,
    enabled: Boolean = true,
    tone: RdioClickTone = RdioClickTone.Click,
) {
    val sound = LocalClickSound.current
    val wrappedOnClick: () -> Unit = {
        if (enabled) {
            when (tone) {
                RdioClickTone.Click -> sound?.click()
                RdioClickTone.Activate -> sound?.activate()
                RdioClickTone.Deactivate -> sound?.deactivate()
                RdioClickTone.Denied -> sound?.denied()
            }
            onClick()
        } else {
            sound?.denied()
        }
    }
    val borderColor = when (state) {
        RdioButtonState.Default -> RdioPalette.BorderSubtle
        RdioButtonState.Off -> Color(0x80EF4444)
        RdioButtonState.On -> Color(0x8022C55E)
        RdioButtonState.Partial -> Color(0x80EAB308)
    }
    val bgBrush = when (state) {
        RdioButtonState.Default -> solidSurface()
        RdioButtonState.Off -> tintBrush(Color(0x33EF4444))
        RdioButtonState.On -> tintBrush(Color(0x3322C55E))
        RdioButtonState.Partial -> tintBrush(Color(0x33EAB308))
    }
    val shape = RoundedCornerShape(10.dp)

    Box(
        modifier = modifier
            .heightIn(min = 56.dp)
            .defaultMinSize(minWidth = 80.dp)
            .clip(shape)
            .background(bgBrush, shape)
            .border(BorderStroke(1.dp, borderColor), shape)
            .clickable(enabled = true, onClick = wrappedOnClick)
            .padding(horizontal = 10.dp, vertical = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = label,
            textAlign = TextAlign.Center,
            color = if (enabled) RdioPalette.TextMain else RdioPalette.TextSoft,
            style = LocalTextStyle.current.copy(
                fontSize = 12.sp,
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 0.6.sp,
                lineHeight = 14.sp,
            ),
        )
        if (state != RdioButtonState.Default) {
            LedCorner(color = ledColorFor(state))
        }
    }
}

@Composable
private fun BoxScope.LedCorner(color: Color) {
    val pulse by rememberInfiniteTransition(label = "led").animateFloat(
        initialValue = 0.55f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation = tween(900),
            repeatMode = RepeatMode.Reverse,
        ),
        label = "led-pulse",
    )
    Box(
        Modifier
            .align(Alignment.TopEnd)
            .padding(6.dp)
            .size(10.dp)
            .drawBehind {
                drawCircle(color.copy(alpha = 0.35f * pulse), radius = size.minDimension * 0.95f)
            },
        contentAlignment = Alignment.Center,
    ) {
        Box(
            Modifier
                .size(6.dp)
                .background(color.copy(alpha = pulse), CircleShape),
        )
    }
}

private fun ledColorFor(state: RdioButtonState): Color = when (state) {
    RdioButtonState.Off -> RdioPalette.Red
    RdioButtonState.On -> RdioPalette.Green
    RdioButtonState.Partial -> RdioPalette.Yellow
    RdioButtonState.Default -> Color.Transparent
}

private fun solidSurface(): Brush = Brush.linearGradient(
    0f to RdioPalette.Surface, 1f to RdioPalette.Surface,
)

private fun tintBrush(tint: Color): Brush = Brush.radialGradient(
    0f to tint,
    1f to RdioPalette.Surface,
    center = Offset(0f, 0f),
    radius = 260f,
)

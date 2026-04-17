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
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.SwapHoriz
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette

/** Dark rounded LCD-style card used for the scanner display. */
@Composable
fun LcdPanel(
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    val shape = RoundedCornerShape(12.dp)
    Column(
        modifier
            .clip(shape)
            .background(RdioPalette.Surface, shape)
            .border(BorderStroke(1.dp, RdioPalette.BorderSubtle), shape)
            .padding(12.dp),
    ) {
        content()
    }
}

/** `.rdio-status` → branding (uppercase, letter-spaced) + LED. */
@Composable
fun StatusBar(
    branding: String,
    ledOn: Boolean,
    ledColor: Color,
    paused: Boolean,
    modifier: Modifier = Modifier,
    onSwitchConnection: (() -> Unit)? = null,
    connectionLabel: String? = null,
) {
    Row(
        modifier = modifier.fillMaxWidth().padding(horizontal = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (onSwitchConnection != null) {
            SwitchConnectionChip(
                label = connectionLabel ?: "CONNECTIONS",
                onClick = onSwitchConnection,
            )
            Spacer(Modifier.size(10.dp))
        }
        Text(
            branding.uppercase(),
            modifier = Modifier.weight(1f),
            color = RdioPalette.TextMuted,
            style = LocalTextStyle.current.copy(
                fontSize = 16.sp,
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 2.sp,
            ),
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        LedIndicator(color = ledColor, on = ledOn, paused = paused)
    }
}

@Composable
private fun SwitchConnectionChip(label: String, onClick: () -> Unit) {
    Row(
        modifier = Modifier
            .clip(RoundedCornerShape(999.dp))
            .background(RdioPalette.Surface, RoundedCornerShape(999.dp))
            .border(BorderStroke(1.dp, RdioPalette.BorderSubtle), RoundedCornerShape(999.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(
            Icons.Default.SwapHoriz,
            contentDescription = null,
            tint = RdioPalette.Accent,
            modifier = Modifier.size(14.dp),
        )
        Spacer(Modifier.size(6.dp))
        Text(
            label.uppercase(),
            color = RdioPalette.TextMain,
            style = LocalTextStyle.current.copy(
                fontSize = 11.sp,
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 1.sp,
            ),
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

@Composable
private fun LedIndicator(color: Color, on: Boolean, paused: Boolean) {
    val blink by rememberInfiniteTransition(label = "led-status").animateFloat(
        initialValue = if (paused) 0f else 1f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation = tween(2000), repeatMode = RepeatMode.Reverse,
        ),
        label = "blink",
    )
    val c = if (on) color else Color(0x4D94A3B8)
    val alpha = if (paused) blink else 1f
    Box(
        Modifier
            .size(14.dp)
            .drawBehind {
                if (on) drawCircle(c.copy(alpha = 0.55f * alpha), radius = size.minDimension * 0.8f)
            },
        contentAlignment = Alignment.Center,
    ) {
        Box(
            Modifier
                .size(12.dp)
                .background(c.copy(alpha = alpha), CircleShape),
        )
    }
}

/** Tiny 14-sp row used throughout the LCD; left child + right child (or just a left). */
@Composable
fun LcdRow(
    left: String,
    right: String? = null,
    size: Float = 14f,
    muted: Boolean = false,
) {
    Row(
        modifier = Modifier.fillMaxWidth().height(20.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        LcdText(left, size, muted)
        if (right != null) LcdText(right, size, muted)
    }
}

@Composable
fun LcdText(
    text: String,
    size: Float = 14f,
    muted: Boolean = false,
    weight: FontWeight = FontWeight.Normal,
    color: Color? = null,
) {
    Text(
        text,
        color = color ?: if (muted) RdioPalette.TextMuted else RdioPalette.TextMain,
        maxLines = 1,
        overflow = TextOverflow.Clip,
        style = TextStyle(
            fontSize = size.sp,
            lineHeight = (size + 6f).sp,
            fontWeight = weight,
        ),
    )
}

/** Big 24-sp row used for the talkgroup name. */
@Composable
fun LcdBigText(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        modifier = modifier.fillMaxWidth().height(32.dp),
        color = RdioPalette.TextMain,
        textAlign = TextAlign.Start,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
        style = TextStyle(
            fontSize = 24.sp,
            lineHeight = 32.sp,
            fontWeight = FontWeight.Medium,
        ),
    )
}

@Composable
fun LcdSpacerSmall() {
    Spacer(Modifier.height(6.dp))
}

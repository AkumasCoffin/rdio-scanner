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
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.SwapHoriz
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette
import solutions.saubeo.rdioscanner.ui.theme.ledColorDimmed

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
    // Dual-color LED (#10): when dualLed is on the round LED becomes a
    // twin-module lightbar, second module in ledColor2, optionally wig-wag
    // flashing. Defaults preserve the classic single LED.
    dualLed: Boolean = false,
    ledColor2: Color = ledColor,
    wigWag: Boolean = false,
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
        if (dualLed) {
            DualLedIndicator(
                color1 = ledColor,
                color2 = ledColor2,
                on = ledOn,
                paused = paused,
                wigWag = wigWag,
            )
        } else {
            LedIndicator(color = ledColor, on = ledOn, paused = paused)
        }
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

/**
 * Twin-module lightbar for dual-color mode — mirrors the webapp's `.led.dual`:
 * two color modules behind one squared lens, split by a 4dp near-black
 * divider, glass highlight across the top, per-module glow. With wigWag on,
 * the modules alternate every 450 ms while a call plays (paused shows both,
 * so the paused blink stays readable).
 */
@Composable
private fun DualLedIndicator(
    color1: Color,
    color2: Color,
    on: Boolean,
    paused: Boolean,
    wigWag: Boolean,
) {
    val blink by rememberInfiniteTransition(label = "led-dual").animateFloat(
        initialValue = if (paused) 0f else 1f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(
            animation = tween(2000), repeatMode = RepeatMode.Reverse,
        ),
        label = "blink",
    )
    val alpha = if (paused) blink else 1f

    val flashing = on && wigWag && !paused
    var leftLit by remember { mutableStateOf(true) }
    LaunchedEffect(flashing) {
        leftLit = true
        while (flashing) {
            delay(450)
            leftLit = !leftLit
        }
    }

    val off = Color(0x4D94A3B8)
    val left = when {
        !on -> off
        flashing && !leftLit -> ledColorDimmed(color1)
        else -> color1
    }
    val right = when {
        !on -> off
        flashing && leftLit -> ledColorDimmed(color2)
        else -> color2
    }
    val leftGlows = on && (!flashing || leftLit)
    val rightGlows = on && (!flashing || !leftLit)

    val shape = RoundedCornerShape(4.dp)
    Box(
        Modifier
            .size(width = 42.dp, height = 18.dp)
            .drawBehind {
                val r = size.height * 0.9f
                if (leftGlows) {
                    drawCircle(
                        color1.copy(alpha = 0.5f * alpha),
                        radius = r,
                        center = Offset(size.width * 0.25f, size.height / 2f),
                    )
                }
                if (rightGlows) {
                    drawCircle(
                        color2.copy(alpha = 0.5f * alpha),
                        radius = r,
                        center = Offset(size.width * 0.75f, size.height / 2f),
                    )
                }
            }
            .clip(shape)
            .background(RdioPalette.Bg)
            .border(BorderStroke(1.dp, Color(0xB3020617)), shape),
    ) {
        Row(Modifier.fillMaxSize()) {
            Box(
                Modifier
                    .weight(1f)
                    .fillMaxHeight()
                    .background(left.copy(alpha = left.alpha * alpha)),
            )
            // The divider: the housing background showing through, like the
            // webapp's ::before seam.
            Spacer(Modifier.width(4.dp).fillMaxHeight())
            Box(
                Modifier
                    .weight(1f)
                    .fillMaxHeight()
                    .background(right.copy(alpha = right.alpha * alpha)),
            )
        }
        // Glass highlight swept across the top of the lens.
        Box(
            Modifier
                .align(Alignment.TopCenter)
                .padding(top = 1.dp, start = 2.dp, end = 2.dp)
                .fillMaxWidth()
                .height(6.dp)
                .clip(RoundedCornerShape(2.dp))
                .background(
                    Brush.verticalGradient(
                        listOf(Color(0x59FFFFFF), Color(0x0AFFFFFF)),
                    ),
                ),
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
    overflow: TextOverflow = TextOverflow.Ellipsis,
    maxLines: Int = 1,
) {
    Text(
        text,
        color = color ?: if (muted) RdioPalette.TextMuted else RdioPalette.TextMain,
        maxLines = maxLines,
        overflow = overflow,
        style = TextStyle(
            fontSize = size.sp,
            lineHeight = (size + 6f).sp,
            fontWeight = weight,
        ),
    )
}

/** Big 24-sp row used for the talkgroup name. Wraps to as many lines as the
 *  name needs — long names like "South Eastern Zone | Rescue" used to clip
 *  at one line, so the user only saw "South Eastern Zone | R…". */
@Composable
fun LcdBigText(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        modifier = modifier.fillMaxWidth().heightIn(min = 32.dp),
        color = RdioPalette.TextMain,
        textAlign = TextAlign.Start,
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

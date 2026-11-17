package solutions.saubeo.rdioscanner.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import solutions.saubeo.rdioscanner.data.protocol.SYSTEM_MESSAGE_KIND_MAINTENANCE
import solutions.saubeo.rdioscanner.data.protocol.SYSTEM_MESSAGE_KIND_SYSTEM
import solutions.saubeo.rdioscanner.data.protocol.SystemMessageDto
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette
import solutions.saubeo.rdioscanner.ui.theme.RdioShape
import solutions.saubeo.rdioscanner.ui.theme.systemMessageColor
import java.text.DateFormat
import java.util.Date

/**
 * The server's system and maintenance messages, shown above the display.
 *
 * Each message that has not been closed is a row of its own. Once any are
 * closed a small "closed messages" button stays behind, and pressing it — or
 * any row — opens a card with every message, the two kinds under separate
 * headings, where a closed one can be shown again.
 *
 * Draws nothing at all when there are no messages, which is always the case on
 * a server without the System Messages plugin.
 */
@Composable
fun SystemMessageStrip(
    messages: List<SystemMessageDto>,
    closed: Set<String>,
    onClose: (SystemMessageDto) -> Unit,
    onReopen: (SystemMessageDto) -> Unit,
    modifier: Modifier = Modifier,
) {
    if (messages.isEmpty()) return

    var showAll by rememberSaveable { mutableStateOf(false) }

    val open = messages.filter { it.token !in closed }
    val closedCount = messages.size - open.size

    Column(modifier = modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        open.forEach { message ->
            MessageRow(
                message = message,
                full = false,
                isClosed = false,
                onClick = { showAll = true },
                onClose = { onClose(message) },
                onReopen = {},
            )
        }

        if (closedCount > 0) {
            Row(
                modifier = Modifier
                    .align(Alignment.End)
                    .clip(RoundedCornerShape(50))
                    .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(50))
                    .clickable(role = Role.Button) { showAll = true }
                    .padding(start = 8.dp, end = 12.dp, top = 6.dp, bottom = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                Icon(Icons.Outlined.Info, contentDescription = null, tint = RdioPalette.TextMuted, modifier = Modifier.size(16.dp))
                Text(
                    text = if (closedCount == 1) "1 closed message" else "$closedCount closed messages",
                    color = RdioPalette.TextMuted,
                    fontSize = 12.sp,
                )
            }
        }
    }

    if (showAll) {
        MessagesCard(
            messages = messages,
            closed = closed,
            onClose = onClose,
            onReopen = onReopen,
            onDismiss = { showAll = false },
        )
    }
}

@Composable
private fun MessagesCard(
    messages: List<SystemMessageDto>,
    closed: Set<String>,
    onClose: (SystemMessageDto) -> Unit,
    onReopen: (SystemMessageDto) -> Unit,
    onDismiss: () -> Unit,
) {
    Dialog(onDismissRequest = onDismiss) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(RdioShape.LG))
                .background(RdioPalette.BgElevatedSoft)
                .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(RdioShape.LG)),
        ) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(start = 16.dp, end = 6.dp, top = 6.dp, bottom = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = "Messages",
                    color = RdioPalette.TextMain,
                    fontSize = 17.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .weight(1f)
                        .semantics { heading() },
                )
                CloseButton(description = "Close", onClick = onDismiss)
            }

            Column(
                modifier = Modifier
                    .heightIn(max = 480.dp)
                    .verticalScroll(rememberScrollState())
                    .padding(start = 16.dp, end = 16.dp, bottom = 16.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                listOf(
                    SYSTEM_MESSAGE_KIND_SYSTEM to "SYSTEM MESSAGES",
                    SYSTEM_MESSAGE_KIND_MAINTENANCE to "MAINTENANCE",
                ).forEach { (kind, heading) ->
                    val ofKind = messages.filter { it.kind == kind }
                    if (ofKind.isEmpty()) return@forEach

                    Text(
                        text = heading,
                        color = RdioPalette.TextMuted,
                        fontSize = 11.sp,
                        fontWeight = FontWeight.Bold,
                        letterSpacing = 1.sp,
                        modifier = Modifier
                            .padding(top = 8.dp)
                            .semantics { heading() },
                    )

                    ofKind.forEach { message ->
                        MessageRow(
                            message = message,
                            full = true,
                            isClosed = message.token in closed,
                            onClick = null,
                            onClose = { onClose(message) },
                            onReopen = { onReopen(message) },
                        )
                    }
                }
            }
        }
    }
}

/**
 * One message. In the strip ([full] false) the text is kept to two lines and
 * the row opens the card; in the card it is shown whole, with its times.
 */
@Composable
private fun MessageRow(
    message: SystemMessageDto,
    full: Boolean,
    isClosed: Boolean,
    onClick: (() -> Unit)?,
    onClose: () -> Unit,
    onReopen: () -> Unit,
) {
    val color = systemMessageColor(message.color, message.kind)
    val shape = RoundedCornerShape(RdioShape.MD)
    val fade = if (isClosed) 0.6f else 1f

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(shape)
            .background(color.copy(alpha = 0.16f * fade))
            .border(1.dp, color.copy(alpha = 0.45f * fade), shape)
            .drawBehind { drawRect(color.copy(alpha = fade), size = Size(4.dp.toPx(), size.height)) }
            .then(if (onClick != null) Modifier.clickable(onClickLabel = "Show all messages", onClick = onClick) else Modifier)
            .padding(start = 14.dp, top = 8.dp, bottom = 8.dp, end = 2.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            if (!full) {
                Text(
                    text = if (message.kind == SYSTEM_MESSAGE_KIND_MAINTENANCE) "MAINTENANCE" else "SYSTEM",
                    color = color,
                    fontSize = 10.sp,
                    fontWeight = FontWeight.Bold,
                    letterSpacing = 0.8.sp,
                )
            }

            Text(
                text = message.title,
                color = RdioPalette.TextMain.copy(alpha = fade),
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )

            if (message.body.isNotBlank()) {
                Text(
                    text = message.body,
                    color = RdioPalette.TextMain.copy(alpha = 0.85f * fade),
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                    maxLines = if (full) Int.MAX_VALUE else 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }

            MessageLink(message)

            if (full) {
                val times = messageTimes(message)
                if (times.isNotEmpty()) {
                    Text(text = times, color = RdioPalette.TextMuted, fontSize = 11.sp)
                }
            }
        }

        Spacer(Modifier.width(4.dp))

        if (full && isClosed) {
            Text(
                text = "Show again",
                color = RdioPalette.Accent,
                fontSize = 13.sp,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .clickable(role = Role.Button, onClick = onReopen)
                    .padding(horizontal = 10.dp, vertical = 12.dp),
            )
        } else {
            CloseButton(description = "Close this message: ${message.title}", onClick = onClose)
        }
    }
}

@Composable
private fun MessageLink(message: SystemMessageDto) {
    // http and https only. The server refuses anything else; this is the same
    // test again, at the point where the address would be opened.
    val url = message.linkUrl.trim()
    if (!url.startsWith("http://", ignoreCase = true) && !url.startsWith("https://", ignoreCase = true)) return

    val uriHandler = LocalUriHandler.current

    Text(
        text = message.linkLabel.ifBlank { "More information" },
        color = RdioPalette.Accent,
        fontSize = 13.sp,
        textDecoration = TextDecoration.Underline,
        modifier = Modifier
            .clickable(role = Role.Button, onClickLabel = "Open link") {
                // No browser installed, or an address nothing can open.
                runCatching { uriHandler.openUri(url) }
            }
            .padding(vertical = 6.dp),
    )
}

@Composable
private fun CloseButton(description: String, onClick: () -> Unit) {
    Box(
        modifier = Modifier
            .size(44.dp)
            .clip(RoundedCornerShape(50))
            .clickable(role = Role.Button, onClickLabel = description, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Icon(Icons.Filled.Close, contentDescription = description, tint = RdioPalette.TextMuted, modifier = Modifier.size(18.dp))
    }
}

private fun messageTimes(message: SystemMessageDto): String {
    val format = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT)
    val parts = mutableListOf<String>()

    message.startsAt?.takeIf { it > 0 }?.let { parts += "Since " + format.format(Date(it.toLong())) }
    message.endsAt?.takeIf { it > 0 }?.let { parts += "until " + format.format(Date(it.toLong())) }

    return parts.joinToString(", ")
}

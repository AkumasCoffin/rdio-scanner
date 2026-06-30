package solutions.saubeo.rdioscanner.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay
import solutions.saubeo.rdioscanner.audio.QueuedCall
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.data.protocol.TalkgroupDto
import solutions.saubeo.rdioscanner.data.repository.HoldState
import solutions.saubeo.rdioscanner.ui.ScannerViewModel
import solutions.saubeo.rdioscanner.ui.components.LcdBigText
import solutions.saubeo.rdioscanner.ui.components.LcdPanel
import solutions.saubeo.rdioscanner.ui.components.LcdRow
import solutions.saubeo.rdioscanner.ui.components.LcdSpacerSmall
import solutions.saubeo.rdioscanner.ui.components.LcdText
import solutions.saubeo.rdioscanner.ui.components.RdioButton
import solutions.saubeo.rdioscanner.ui.components.RdioButtonState
import solutions.saubeo.rdioscanner.ui.components.RdioClickTone
import solutions.saubeo.rdioscanner.ui.components.StatusBar
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette
import solutions.saubeo.rdioscanner.ui.theme.ledColor
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlin.math.roundToInt

@Composable
fun LivefeedScreen(
    vm: ScannerViewModel,
    onOpenSelector: () -> Unit,
    onOpenSearch: () -> Unit,
) {
    val state by vm.state.collectAsStateWithLifecycle()
    val config by vm.config.collectAsStateWithLifecycle()
    val playing by vm.playing.collectAsStateWithLifecycle()
    val queue by vm.queue.collectAsStateWithLifecycle()
    val history by vm.history.collectAsStateWithLifecycle()
    val paused by vm.paused.collectAsStateWithLifecycle()
    val held by vm.held.collectAsStateWithLifecycle()
    val listeners by vm.listeners.collectAsStateWithLifecycle()
    val active by vm.livefeedActive.collectAsStateWithLifecycle()
    val livefeedEnabled by vm.livefeedEnabled.collectAsStateWithLifecycle()
    val avoided by vm.avoided.collectAsStateWithLifecycle()
    val transcripts by vm.transcripts.collectAsStateWithLifecycle()
    val delayMs by vm.delayMs.collectAsStateWithLifecycle()
    val jumpFlashMs by vm.jumpFlashMs.collectAsStateWithLifecycle()
    val autoJump by vm.autoJump.collectAsStateWithLifecycle()
    val autoJumpThreshold by vm.autoJumpThreshold.collectAsStateWithLifecycle()

    val branding = config?.branding?.takeIf { it.isNotBlank() } ?: "Rdio Scanner"
    val showListeners = config?.showListenersCount == true
    val profiles by vm.profiles.collectAsStateWithLifecycle()
    val lastProfileId by vm.lastProfileId.collectAsStateWithLifecycle()
    val activeProfileName = remember(profiles, lastProfileId) {
        profiles.firstOrNull { it.id == lastProfileId }?.name
    }

    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(Unit) {
        while (true) {
            now = System.currentTimeMillis()
            delay(1000)
        }
    }
    val timeFmt = remember { SimpleDateFormat("h:mm:ss a", Locale.getDefault()) }
    val dateFmt = remember { SimpleDateFormat("MM/dd", Locale.getDefault()) }

    val currentSys: SystemDto? = config?.systems?.firstOrNull { it.id == playing?.call?.system }
    val currentTg: TalkgroupDto? = currentSys?.talkgroups?.firstOrNull { it.id == playing?.call?.talkgroup }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .widthIn(max = 640.dp)
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 20.dp, vertical = 24.dp),
        verticalArrangement = Arrangement.spacedBy(20.dp),
    ) {
        StatusBar(
            branding = branding,
            ledOn = playing != null,
            ledColor = ledColor(currentSys?.led),
            paused = paused,
            onSwitchConnection = { vm.disconnect() },
            connectionLabel = activeProfileName ?: "Connections",
        )

        // Live-transcript lookup: prefer the cache (latest TRX wins),
        // fall back to whatever came inline on the CAL.
        val activeTranscript = playing?.call?.let {
            transcripts[it.id]?.takeIf { t -> t.isNotBlank() } ?: it.transcript
        }

        // Kick a one-shot TRX request for the playing call whenever the
        // server hasn't given us a transcript yet. Repository handles
        // dedup; the server replies via the same TRX flow. Triggered on
        // call-id change so each new call gets exactly one initial ask.
        val playingId = playing?.call?.id
        LaunchedEffect(playingId, activeTranscript) {
            val id = playingId ?: return@LaunchedEffect
            if (id <= 0) return@LaunchedEffect
            if (activeTranscript?.isNotBlank() == true) return@LaunchedEffect
            // Tiny delay so Whisper has a head start vs. spamming the
            // server on every CAL. 4s matches the webapp's poll floor.
            delay(4000)
            if (transcripts[id].isNullOrBlank()) {
                vm.requestTranscript(id)
            }
        }

        LcdPanel(Modifier.fillMaxWidth()) {
            DisplayRows(
                now = now,
                timeFmt = timeFmt,
                dateFmt = dateFmt,
                linked = state is ConnectionState.Connected,
                listeners = listeners,
                showListeners = showListeners,
                queueSize = queue.size,
                delayMs = delayMs,
                jumpFlashMs = jumpFlashMs,
                playing = playing,
                config = config,
                system = currentSys,
                talkgroup = currentTg,
                held = held,
                transcript = activeTranscript,
            )
            Spacer(Modifier.height(10.dp))
            HistoryTable(
                history = history,
                timeFmt = timeFmt,
                currentId = playing?.call?.id,
                transcripts = transcripts,
            )
        }

        val hasCallContext = playing != null || history.isNotEmpty()
        ControlGrid(
            livefeedEnabled = livefeedEnabled,
            livefeedActive = active,
            paused = paused,
            held = held,
            hasPlaying = playing != null,
            hasCallContext = hasCallContext,
            anyAvoided = avoided.isNotEmpty(),
            autoJump = autoJump,
            autoJumpSuspended = autoJump && held != HoldState.None,
            autoJumpThreshold = autoJumpThreshold,
            onLiveFeed = vm::toggleLivefeed,
            onHoldSys = vm::holdSystem,
            onHoldTg = vm::holdTalkgroup,
            onReplay = vm::replayLast,
            onSkip = vm::skip,
            onAvoid = vm::avoidCurrent,
            onPause = vm::togglePause,
            onSelectTg = onOpenSelector,
            onSearch = onOpenSearch,
            onClearAvoids = vm::clearAvoids,
            onAutoJump = vm::toggleAutoJump,
            onAutoJumpThreshold = vm::setAutoJumpThreshold,
        )
    }
}

@Composable
private fun DisplayRows(
    now: Long,
    timeFmt: SimpleDateFormat,
    dateFmt: SimpleDateFormat,
    linked: Boolean,
    listeners: Int,
    showListeners: Boolean,
    queueSize: Int,
    delayMs: Long,
    jumpFlashMs: Long,
    playing: QueuedCall?,
    config: ConfigDto?,
    system: SystemDto?,
    talkgroup: TalkgroupDto?,
    held: HoldState,
    transcript: String?,
) {
    val call: CallDto? = playing?.call
    val rightTop = buildString {
        when {
            !linked -> append("NO LINK")
            showListeners -> append("L: $listeners")
        }
    }

    LcdRow(left = timeFmt.format(Date(now)), right = "Queue: $queueSize")
    val delayText = formatDelayMs(delayMs)
    if (delayText.isNotEmpty()) {
        val removedText = formatDelayMs(jumpFlashMs)
        Row(
            Modifier.fillMaxWidth().height(16.dp),
            horizontalArrangement = Arrangement.End,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            LcdText(
                text = "Delay: $delayText",
                size = 12f,
                weight = FontWeight.SemiBold,
                color = RdioPalette.Accent,
            )
            if (removedText.isNotEmpty()) {
                Spacer(Modifier.width(6.dp))
                LcdText(
                    text = "-$removedText",
                    size = 12f,
                    weight = FontWeight.Bold,
                    color = RdioPalette.Red,
                )
            }
        }
    }
    LcdSpacerSmall()
    LcdRow(
        left = system?.label ?: "—",
        right = talkgroup?.tag ?: "",
        muted = call == null,
    )
    LcdRow(
        left = talkgroup?.label ?: (call?.let { "TG ${it.talkgroup}" } ?: "—"),
        right = call?.dateTime?.let { iso ->
            val parsed = parseIso(iso)
            if (parsed != null) "${dateFmt.format(parsed)}  ${timeFmt.format(parsed)}" else ""
        } ?: "",
        muted = call == null,
    )
    LcdBigText(talkgroup?.name?.ifBlank { null } ?: talkgroup?.label ?: "Idle")
    LcdRow(
        left = "F: ${formatFrequency(call?.frequency)}",
        right = "TGID: ${call?.talkgroup ?: 0}",
    )
    LcdRow(
        left = "E: 0  S: 0",
        right = call?.source?.let { "UID: $it" } ?: "",
    )
    Row(
        Modifier.fillMaxWidth().height(18.dp),
        horizontalArrangement = Arrangement.End,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (held is HoldState.Talkgroup || held is HoldState.System) {
            HoldFlag(text = if (held is HoldState.System) "HOLD SYS" else "HOLD TG")
        }
        if (call?.patches?.isNotEmpty() == true) {
            Spacer(Modifier.fillMaxWidth(0f))
            HoldFlag(text = "PATCH")
        }
    }
    LcdSpacerSmall()
    TranscriptPanel(
        transcript = transcript,
        ledColor = ledColor(system?.led),
    )
}

/**
 * Live-transcript panel under the call info, mirroring the webapp's
 * live-transcript box. Reserves two lines of vertical space so the
 * LCD layout doesn't shift when text arrives, shows an "—" placeholder
 * when empty, and uses the active system / talkgroup LED color as the
 * left-border tint.
 */
@Composable
private fun TranscriptPanel(
    transcript: String?,
    ledColor: Color,
) {
    val text = transcript?.trim().orEmpty()
    val hasText = text.isNotBlank()
    val borderTint = if (hasText) ledColor.copy(alpha = 0.55f) else Color(0x33FFFFFF)
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = 56.dp)
            .clip(RoundedCornerShape(6.dp))
            .background(Color(0x33000000), RoundedCornerShape(6.dp))
            .border(1.dp, borderTint, RoundedCornerShape(6.dp))
            .padding(horizontal = 10.dp, vertical = 6.dp),
        verticalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        LcdText(
            text = "TRANSCRIPT",
            size = 9f,
            weight = FontWeight.Bold,
            color = if (hasText) ledColor else RdioPalette.TextMuted,
        )
        if (hasText) {
            // No maxLines cap: long transcripts should show in full so the
            // user can read everything Whisper returned without scrolling
            // to a different screen.
            Text(
                text = text,
                color = RdioPalette.TextMain,
                style = TextStyle(
                    fontSize = 13.sp,
                    lineHeight = 17.sp,
                    fontWeight = FontWeight.Normal,
                ),
                minLines = 2,
            )
        } else {
            Text(
                text = "—",
                color = RdioPalette.TextSoft,
                style = TextStyle(
                    fontSize = 13.sp,
                    lineHeight = 17.sp,
                    fontWeight = FontWeight.Normal,
                ),
                minLines = 2,
            )
        }
    }
}

@Composable
private fun HoldFlag(text: String) {
    Box(
        Modifier
            .padding(start = 6.dp)
            .clip(RoundedCornerShape(4.dp))
            .background(Color(0x4DEF4444))
            .border(width = 1.dp, color = Color(0x80EF4444), shape = RoundedCornerShape(4.dp))
            .padding(horizontal = 6.dp, vertical = 1.dp),
    ) {
        LcdText(
            text = text,
            size = 10f,
            weight = FontWeight.Bold,
            color = Color(0xFFFCA5A5),
        )
    }
}

@Composable
private fun HistoryTable(
    history: List<QueuedCall>,
    timeFmt: SimpleDateFormat,
    currentId: Long?,
    transcripts: Map<Long, String> = emptyMap(),
) {
    Row(
        Modifier.fillMaxWidth().padding(top = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        // Name column gets the biggest share — talkgroup-name strings
        // ("South Eastern Zone | Rescue") are typically 2–4× longer than
        // the abbreviated System / Talkgroup labels.
        LcdHeader("Time", weight = 0.22f)
        LcdHeader("System", weight = 0.20f)
        LcdHeader("Talkgroup", weight = 0.20f)
        LcdHeader("Name", weight = 0.38f)
    }
    Spacer(Modifier.height(2.dp))
    if (history.isEmpty()) {
        Row(Modifier.fillMaxWidth().height(22.dp), verticalAlignment = Alignment.CenterVertically) {
            LcdText(text = "—", size = 11f, muted = true)
        }
        return
    }
    // Bounded LazyColumn so the history box never grows tall enough to
    // push the control grid below the fold — the user scrolls within
    // this box to reach older calls. heightIn(max) is required for
    // LazyColumn nested inside the outer screen's verticalScroll
    // (Compose would otherwise complain about infinity height
    // constraints). ~5 rows visible without their transcript snippets;
    // fewer when snippets push row heights up.
    val listState = rememberLazyListState()
    val topId = history.firstOrNull()?.call?.id

    // Stick to the top when a new call arrives, but only if the user
    // hadn't scrolled away. Without this LazyColumn anchors to the
    // previously-visible key, so a new call silently lands offscreen
    // above the viewport and the user sees a stale top row.
    LaunchedEffect(topId) {
        if (topId != null &&
            listState.firstVisibleItemIndex <= 1 &&
            listState.firstVisibleItemScrollOffset < 64
        ) {
            listState.scrollToItem(0)
        }
    }

    LazyColumn(
        state = listState,
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(max = 220.dp)
            .drawWithContent {
                drawContent()
                // Custom scrollbar: only paint when content overflows.
                // Thumb height ≈ viewport / content, position scales with
                // first-visible item + offset.
                val info = listState.layoutInfo
                val totalItems = info.totalItemsCount
                val visible = info.visibleItemsInfo
                if (totalItems == 0 || visible.isEmpty()) return@drawWithContent
                if (totalItems <= visible.size) return@drawWithContent
                val avgItem = visible.sumOf { it.size }.toFloat() / visible.size
                if (avgItem <= 0f) return@drawWithContent
                val totalHeight = avgItem * totalItems
                val viewport = size.height
                if (totalHeight <= viewport) return@drawWithContent
                val thumbH = (viewport * viewport / totalHeight).coerceAtLeast(24f)
                val scrolled = listState.firstVisibleItemIndex * avgItem +
                    listState.firstVisibleItemScrollOffset
                val progress = (scrolled / (totalHeight - viewport)).coerceIn(0f, 1f)
                val thumbY = progress * (viewport - thumbH)
                val trackWidth = 3f
                drawRoundRect(
                    color = Color(0x33FFFFFF),
                    topLeft = Offset(size.width - trackWidth - 1f, 0f),
                    size = Size(trackWidth, viewport),
                    cornerRadius = CornerRadius(trackWidth / 2f, trackWidth / 2f),
                )
                drawRoundRect(
                    color = Color(0xCCFCA5A5),
                    topLeft = Offset(size.width - trackWidth - 1f, thumbY),
                    size = Size(trackWidth, thumbH),
                    cornerRadius = CornerRadius(trackWidth / 2f, trackWidth / 2f),
                )
            },
    ) {
        items(history, key = { it.call.id }) { item ->
            HistoryRow(
                item = item,
                timeFmt = timeFmt,
                replaying = currentId != null && item.call.id == currentId,
                transcripts = transcripts,
            )
        }
    }
}

@Composable
private fun HistoryRow(
    item: QueuedCall,
    timeFmt: SimpleDateFormat,
    replaying: Boolean,
    transcripts: Map<Long, String>,
) {
    val rowBackground = if (replaying) Color(0x22F97316) else Color.Transparent
    // Was height(22.dp) — too short once the Name column wraps. Use a min
    // height so single-line rows still look uniform but a long talkgroup
    // name can grow the row vertically.
    Row(
        Modifier
            .fillMaxWidth()
            .heightIn(min = 22.dp)
            .background(rowBackground)
            .padding(vertical = 2.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.Top,
    ) {
        val ts = parseIso(item.call.dateTime)?.let(timeFmt::format).orEmpty()
        HistoryCell(ts, weight = 0.22f, highlight = replaying)
        HistoryCell(item.systemLabel ?: "${item.call.system}", weight = 0.20f, highlight = replaying)
        HistoryCell(item.talkgroupLabel ?: "${item.call.talkgroup}", weight = 0.20f, highlight = replaying)
        HistoryCell(
            item.talkgroupName?.ifBlank { null }
                ?: item.call.frequency?.let { formatFrequency(it) }
                ?: "",
            weight = 0.38f,
            highlight = replaying,
            // Talkgroup names like "South Eastern Zone | Rescue" used to
            // ellipsize at one line; unbounded here so the full name shows
            // and the row grows to fit.
            maxLines = Int.MAX_VALUE,
        )
    }
    val historyTranscript = transcripts[item.call.id]?.takeIf { it.isNotBlank() }
        ?: item.call.transcript
    historyTranscript?.trim()?.takeIf { it.isNotBlank() }?.let { snippet ->
        Row(
            Modifier
                .fillMaxWidth()
                .background(rowBackground)
                .padding(start = 6.dp, end = 6.dp, bottom = 2.dp),
        ) {
            // No maxLines cap — show the full transcript snippet under the
            // row so the user can read everything without opening Search.
            Text(
                text = snippet,
                color = if (replaying) RdioPalette.Accent.copy(alpha = 0.85f) else RdioPalette.TextMuted,
                style = TextStyle(
                    fontSize = 10.5.sp,
                    lineHeight = 13.sp,
                    fontWeight = FontWeight.Normal,
                ),
            )
        }
    }
}

@Composable
private fun androidx.compose.foundation.layout.RowScope.LcdHeader(label: String, weight: Float) {
    Box(Modifier.weight(weight)) {
        LcdText(text = label.uppercase(), size = 10f, muted = true, weight = FontWeight.SemiBold)
    }
}

@Composable
private fun androidx.compose.foundation.layout.RowScope.HistoryCell(
    text: String,
    weight: Float,
    highlight: Boolean,
    maxLines: Int = 1,
) {
    Box(Modifier.weight(weight)) {
        LcdText(
            text = text,
            size = 11f,
            weight = if (highlight) FontWeight.Bold else FontWeight.Normal,
            color = if (highlight) RdioPalette.Accent else RdioPalette.TextMain,
            maxLines = maxLines,
        )
    }
}

@Composable
private fun ControlGrid(
    livefeedEnabled: Boolean,
    livefeedActive: Boolean,
    paused: Boolean,
    held: HoldState,
    hasPlaying: Boolean,
    hasCallContext: Boolean,
    anyAvoided: Boolean,
    autoJump: Boolean,
    autoJumpSuspended: Boolean,
    autoJumpThreshold: Int,
    onLiveFeed: () -> Unit,
    onHoldSys: () -> Unit,
    onHoldTg: () -> Unit,
    onReplay: () -> Unit,
    onSkip: () -> Unit,
    onAvoid: () -> Unit,
    onPause: () -> Unit,
    onSelectTg: () -> Unit,
    onSearch: () -> Unit,
    onClearAvoids: () -> Unit,
    onAutoJump: () -> Unit,
    onAutoJumpThreshold: (Int) -> Unit,
) {
    val row: @Composable (List<@Composable (Modifier) -> Unit>) -> Unit = { buttons ->
        Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            buttons.forEach { btn -> btn(Modifier.weight(1f)) }
        }
    }

    val liveState = when {
        !livefeedEnabled -> RdioButtonState.Off
        livefeedActive -> RdioButtonState.On
        else -> RdioButtonState.Partial
    }

    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        row(listOf(
            { m ->
                RdioButton(
                    label = "LIVE\nFEED",
                    onClick = onLiveFeed,
                    modifier = m,
                    state = liveState,
                    tone = if (livefeedEnabled) RdioClickTone.Deactivate else RdioClickTone.Activate,
                )
            },
            { m ->
                RdioButton(
                    label = "HOLD\nSYS",
                    onClick = onHoldSys,
                    modifier = m,
                    state = if (held is HoldState.System) RdioButtonState.On else RdioButtonState.Off,
                    enabled = hasCallContext || held is HoldState.System,
                    tone = if (held is HoldState.System) RdioClickTone.Deactivate else RdioClickTone.Activate,
                )
            },
            { m ->
                RdioButton(
                    label = "HOLD\nTG",
                    onClick = onHoldTg,
                    modifier = m,
                    state = if (held is HoldState.Talkgroup) RdioButtonState.On else RdioButtonState.Off,
                    enabled = hasCallContext || held is HoldState.Talkgroup,
                    tone = if (held is HoldState.Talkgroup) RdioClickTone.Deactivate else RdioClickTone.Activate,
                )
            },
        ))
        row(listOf(
            { m ->
                RdioButton(
                    label = "REPLAY\nLAST",
                    onClick = onReplay,
                    modifier = m,
                    enabled = hasCallContext && !paused,
                    tone = RdioClickTone.Activate,
                )
            },
            { m ->
                RdioButton(
                    label = "SKIP\nNEXT",
                    onClick = onSkip,
                    modifier = m,
                    tone = RdioClickTone.Activate,
                )
            },
            { m ->
                RdioButton(
                    label = "AVOID",
                    onClick = onAvoid,
                    modifier = m,
                    enabled = hasCallContext,
                    tone = RdioClickTone.Activate,
                )
            },
        ))
        row(listOf(
            { m -> RdioButton(label = "SEARCH\nCALL", onClick = onSearch, modifier = m) },
            { m ->
                RdioButton(
                    label = "PAUSE",
                    onClick = onPause,
                    modifier = m,
                    state = if (paused) RdioButtonState.On else RdioButtonState.Off,
                    tone = if (paused) RdioClickTone.Activate else RdioClickTone.Deactivate,
                )
            },
            { m -> RdioButton(label = "SELECT\nTG", onClick = onSelectTg, modifier = m) },
        ))
        // Auto-jump toggle + threshold slider. Off = red, suspended-by-hold =
        // yellow, active = green. The slider dims when auto-jump is off.
        val autoJumpState = when {
            !autoJump -> RdioButtonState.Off
            autoJumpSuspended -> RdioButtonState.Partial
            else -> RdioButtonState.On
        }
        var sliderPos by remember(autoJumpThreshold) {
            mutableFloatStateOf(autoJumpThreshold.toFloat())
        }
        Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            RdioButton(
                label = "AUTO\nJUMP",
                onClick = onAutoJump,
                modifier = Modifier.weight(1f),
                state = autoJumpState,
                tone = if (autoJump) RdioClickTone.Deactivate else RdioClickTone.Activate,
            )
            Column(
                modifier = Modifier
                    .weight(2f)
                    .alpha(if (autoJump) 1f else 0.4f),
                verticalArrangement = Arrangement.spacedBy(2.dp),
            ) {
                LcdText(
                    text = "JUMP AT  ${sliderPos.roundToInt()} MIN",
                    size = 11f,
                    muted = true,
                    weight = FontWeight.SemiBold,
                )
                Slider(
                    value = sliderPos,
                    onValueChange = { sliderPos = it },
                    onValueChangeFinished = { onAutoJumpThreshold(sliderPos.roundToInt()) },
                    valueRange = 1f..10f,
                    steps = 8,
                    colors = SliderDefaults.colors(
                        thumbColor = RdioPalette.Accent,
                        activeTrackColor = RdioPalette.Accent,
                    ),
                )
            }
        }
        if (anyAvoided) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                RdioButton(
                    label = "CLEAR\nAVOIDS",
                    onClick = onClearAvoids,
                    modifier = Modifier.weight(1f),
                )
                Spacer(Modifier.weight(2f))
            }
        }
    }
}

/** Formats a delay in ms as M:SS (or H:MM:SS past an hour). Empty when <= 0. */
private fun formatDelayMs(ms: Long): String {
    val total = (ms / 1000).toInt()
    if (total <= 0) return ""
    val hours = total / 3600
    val minutes = (total % 3600) / 60
    val seconds = total % 60
    return if (hours > 0) {
        "%d:%02d:%02d".format(hours, minutes, seconds)
    } else {
        "%d:%02d".format(minutes, seconds)
    }
}

private fun parseIso(iso: String): Date? = solutions.saubeo.rdioscanner.util.parseIsoInstant(iso)

private fun formatFrequency(f: Double?): String {
    if (f == null || f == 0.0) return "0"
    // server sends Hz; webapp shows the raw number
    return f.toLong().toString()
}

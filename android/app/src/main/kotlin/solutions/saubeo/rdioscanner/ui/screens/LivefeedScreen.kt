package solutions.saubeo.rdioscanner.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.rememberScrollState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
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
    val timeFmt = remember { SimpleDateFormat("HH:mm:ss", Locale.getDefault()) }
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

        LcdPanel(Modifier.fillMaxWidth()) {
            DisplayRows(
                now = now,
                timeFmt = timeFmt,
                dateFmt = dateFmt,
                linked = state is ConnectionState.Connected,
                listeners = listeners,
                showListeners = showListeners,
                queueSize = queue.size,
                playing = playing,
                config = config,
                system = currentSys,
                talkgroup = currentTg,
                held = held,
            )
            Spacer(Modifier.height(10.dp))
            HistoryTable(history = history, timeFmt = timeFmt, currentId = playing?.call?.id)
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
    playing: QueuedCall?,
    config: ConfigDto?,
    system: SystemDto?,
    talkgroup: TalkgroupDto?,
    held: HoldState,
) {
    val call: CallDto? = playing?.call
    val rightTop = buildString {
        when {
            !linked -> append("NO LINK")
            showListeners -> append("L: $listeners")
        }
    }

    LcdRow(left = timeFmt.format(Date(now)), right = rightTop.ifBlank { null })
    LcdRow(left = "", right = "Q: $queueSize")
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
) {
    Row(
        Modifier.fillMaxWidth().padding(top = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        LcdHeader("Time", weight = 0.18f)
        LcdHeader("System", weight = 0.28f)
        LcdHeader("Talkgroup", weight = 0.22f)
        LcdHeader("Name", weight = 0.32f)
    }
    Spacer(Modifier.height(2.dp))
    if (history.isEmpty()) {
        Row(Modifier.fillMaxWidth().height(22.dp), verticalAlignment = Alignment.CenterVertically) {
            LcdText(text = "—", size = 11f, muted = true)
        }
        return
    }
    history.forEach { item ->
        val replaying = currentId != null && item.call.id == currentId
        Row(
            Modifier
                .fillMaxWidth()
                .height(22.dp)
                .background(if (replaying) Color(0x22F97316) else Color.Transparent),
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            val ts = parseIso(item.call.dateTime)?.let(timeFmt::format).orEmpty()
            HistoryCell(ts, weight = 0.18f, highlight = replaying)
            HistoryCell(item.systemLabel ?: "${item.call.system}", weight = 0.28f, highlight = replaying)
            HistoryCell(item.talkgroupLabel ?: "${item.call.talkgroup}", weight = 0.22f, highlight = replaying)
            HistoryCell(
                item.talkgroupName?.ifBlank { null }
                    ?: item.call.frequency?.let { formatFrequency(it) }
                    ?: "",
                weight = 0.32f,
                highlight = replaying,
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
) {
    Box(Modifier.weight(weight)) {
        LcdText(
            text = text,
            size = 11f,
            weight = if (highlight) FontWeight.Bold else FontWeight.Normal,
            color = if (highlight) RdioPalette.Accent else RdioPalette.TextMain,
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

private fun parseIso(iso: String): Date? {
    return runCatching {
        SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.US).parse(iso)
    }.getOrNull() ?: runCatching {
        SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", Locale.US).parse(iso)
    }.getOrNull()
}

private fun formatFrequency(f: Double?): String {
    if (f == null || f == 0.0) return "0"
    // server sends Hz; webapp shows the raw number
    return f.toLong().toString()
}

@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

package solutions.saubeo.rdioscanner.ui.screens

import android.widget.Toast
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.CalendarMonth
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Download
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DatePicker
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.rememberDatePickerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import solutions.saubeo.rdioscanner.data.protocol.SearchOptions
import solutions.saubeo.rdioscanner.data.protocol.SearchResultCall
import solutions.saubeo.rdioscanner.data.protocol.SearchResults
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.data.repository.DownloadEvent
import solutions.saubeo.rdioscanner.ui.ScannerViewModel
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import java.util.TimeZone

private const val PAGE_SIZE = 100

private data class SearchFilters(
    val system: SystemDto? = null,
    val talkgroup: Int? = null,
    val group: String? = null,
    val tag: String? = null,
    val date: Date? = null,
    val sortDescending: Boolean = true,
) {
    fun isActive(): Boolean = system != null || talkgroup != null ||
        group != null || tag != null || date != null
}

@Composable
fun SearchScreen(
    vm: ScannerViewModel,
    onBack: () -> Unit,
) {
    val context = LocalContext.current
    val config by vm.config.collectAsStateWithLifecycle()
    val systems = config?.systems.orEmpty()
    val groups = remember(config) { config?.groups?.keys?.sorted().orEmpty() }
    val tags = remember(config) { config?.tags?.keys?.sorted().orEmpty() }

    // Plain collectAsState (not lifecycle-gated): when the composable is first
    // created during a navigation animation, the StateFlow update can land in
    // the brief window before lifecycle reaches STARTED; the gated variant can
    // silently miss it.
    val resultsOrNull by vm.searchResults.collectAsState()
    val searching by vm.searching.collectAsState()
    val results = resultsOrNull ?: SearchResults()

    var filters by remember { mutableStateOf(SearchFilters()) }
    var offset by remember { mutableIntStateOf(0) }

    LaunchedEffect(filters, offset) {
        vm.runSearch(
            SearchOptions(
                limit = PAGE_SIZE,
                offset = offset,
                sort = if (filters.sortDescending) -1 else 1,
                system = filters.system?.id,
                talkgroup = filters.talkgroup,
                group = filters.group,
                tag = filters.tag,
                date = filters.date?.let { formatRfc3339(it) },
            )
        )
    }

    LaunchedEffect(Unit) {
        vm.downloads.collect { evt ->
            val msg = when (evt) {
                is DownloadEvent.Saved -> "Saved ${evt.fileName}"
                is DownloadEvent.Failed -> "Download failed: ${evt.reason}"
            }
            Toast.makeText(context, msg, Toast.LENGTH_SHORT).show()
        }
    }

    val total = results.count
    val currentPage by remember(offset) { derivedStateOf { offset / PAGE_SIZE } }
    val pageCount = if (total == 0) 1 else ((total + PAGE_SIZE - 1) / PAGE_SIZE)

    Scaffold(
        containerColor = Color.Transparent,
        topBar = {
            TopAppBar(
                title = { Text("Search Calls") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = null,
                            tint = RdioPalette.TextMain,
                        )
                    }
                },
                actions = {
                    if (filters.isActive()) {
                        TextButton(onClick = { filters = SearchFilters(sortDescending = filters.sortDescending); offset = 0 }) {
                            Text("Reset", color = RdioPalette.Accent)
                        }
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = Color.Transparent,
                    titleContentColor = RdioPalette.TextMain,
                ),
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            FiltersPanel(
                systems = systems,
                groups = groups,
                tags = tags,
                filters = filters,
                onChange = { f ->
                    filters = f
                    offset = 0
                },
                dateRange = results.dateStart?.let(::parseIso) to results.dateStop?.let(::parseIso),
            )

            if (searching) {
                LinearProgressIndicator(
                    modifier = Modifier.fillMaxWidth(),
                    color = RdioPalette.Accent,
                    trackColor = RdioPalette.BgElevatedSoft,
                )
            }

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    "$total call${if (total == 1) "" else "s"}",
                    color = RdioPalette.TextMuted,
                    style = LocalTextStyle.current.copy(fontSize = 12.sp),
                )
                Row(verticalAlignment = Alignment.CenterVertically) {
                    IconButton(
                        enabled = offset > 0,
                        onClick = { offset = (offset - PAGE_SIZE).coerceAtLeast(0) },
                    ) {
                        Icon(
                            Icons.Default.ArrowUpward,
                            modifier = Modifier.size(16.dp),
                            contentDescription = "Previous page",
                            tint = if (offset > 0) RdioPalette.Accent else RdioPalette.TextSoft,
                        )
                    }
                    Text(
                        "${currentPage + 1} / $pageCount",
                        color = RdioPalette.TextMuted,
                        style = LocalTextStyle.current.copy(fontSize = 12.sp),
                    )
                    IconButton(
                        enabled = offset + PAGE_SIZE < total,
                        onClick = { offset += PAGE_SIZE },
                    ) {
                        Icon(
                            Icons.Default.ArrowDownward,
                            modifier = Modifier.size(16.dp),
                            contentDescription = "Next page",
                            tint = if (offset + PAGE_SIZE < total) RdioPalette.Accent else RdioPalette.TextSoft,
                        )
                    }
                }
            }

            if (results.results.isEmpty()) {
                Box(
                    Modifier
                        .fillMaxWidth()
                        .heightIn(min = 180.dp)
                        .clip(RoundedCornerShape(12.dp))
                        .background(RdioPalette.BgElevated, RoundedCornerShape(12.dp))
                        .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(12.dp)),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        when {
                            searching -> "Loading calls…"
                            resultsOrNull == null -> "Loading calls…"
                            else -> "No calls match these filters"
                        },
                        color = RdioPalette.TextMuted,
                    )
                }
            } else {
                LazyColumn(
                    modifier = Modifier.fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    items(results.results, key = { it.id }) { call ->
                        ResultRow(
                            call = call,
                            systems = systems,
                            onPlay = { vm.playSearchResult(call.id) },
                            onDownload = { vm.downloadSearchResult(call.id) },
                        )
                    }
                }
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun FiltersPanel(
    systems: List<SystemDto>,
    groups: List<String>,
    tags: List<String>,
    filters: SearchFilters,
    onChange: (SearchFilters) -> Unit,
    dateRange: Pair<Date?, Date?>,
) {
    Column(
        Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .background(RdioPalette.BgElevated, RoundedCornerShape(14.dp))
            .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(14.dp))
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        FlowRow(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            SortToggle(
                descending = filters.sortDescending,
                onToggle = { onChange(filters.copy(sortDescending = !filters.sortDescending)) },
            )
            DateChip(
                date = filters.date,
                dateRange = dateRange,
                onPick = { onChange(filters.copy(date = it)) },
            )
            EnumDropdown(
                label = filters.system?.label ?: "All systems",
                options = listOf<SystemDto?>(null) + systems,
                renderOption = { if (it == null) "All systems" else it.label },
                selected = filters.system,
                onSelect = { picked ->
                    onChange(filters.copy(system = picked, talkgroup = null))
                },
            )
            val talkgroups = filters.system?.talkgroups.orEmpty()
            val tgLabel = filters.system?.talkgroups?.firstOrNull { it.id == filters.talkgroup }
                ?.let { talkgroupDisplay(it) }
                ?: "All talkgroups"
            EnumDropdown(
                label = tgLabel,
                options = listOf<Int?>(null) + talkgroups.map { it.id },
                renderOption = { id ->
                    if (id == null) {
                        "All talkgroups"
                    } else {
                        talkgroups.firstOrNull { it.id == id }
                            ?.let { talkgroupDisplay(it) }
                            ?: "TG $id"
                    }
                },
                selected = filters.talkgroup,
                enabled = filters.system != null && talkgroups.isNotEmpty(),
                onSelect = { id -> onChange(filters.copy(talkgroup = id)) },
            )
            EnumDropdown(
                label = filters.group ?: "All groups",
                options = listOf<String?>(null) + groups,
                renderOption = { it ?: "All groups" },
                selected = filters.group,
                enabled = groups.isNotEmpty(),
                onSelect = { onChange(filters.copy(group = it)) },
            )
            EnumDropdown(
                label = filters.tag ?: "All tags",
                options = listOf<String?>(null) + tags,
                renderOption = { it ?: "All tags" },
                selected = filters.tag,
                enabled = tags.isNotEmpty(),
                onSelect = { onChange(filters.copy(tag = it)) },
            )
        }
    }
}

@Composable
private fun SortToggle(descending: Boolean, onToggle: () -> Unit) {
    PillButton(
        label = if (descending) "Newest first" else "Oldest first",
        onClick = onToggle,
        leading = if (descending) Icons.Default.ArrowDownward else Icons.Default.ArrowUpward,
    )
}

@Composable
private fun DateChip(
    date: Date?,
    dateRange: Pair<Date?, Date?>,
    onPick: (Date?) -> Unit,
) {
    var showDialog by remember { mutableStateOf(false) }
    val label = remember(date) {
        if (date == null) "Any date"
        else SimpleDateFormat("MMM d, yyyy", Locale.getDefault()).format(date)
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        PillButton(
            label = label,
            onClick = { showDialog = true },
            leading = Icons.Default.CalendarMonth,
        )
        if (date != null) {
            IconButton(onClick = { onPick(null) }) {
                Icon(Icons.Default.Close, contentDescription = "Clear date", tint = RdioPalette.TextMuted)
            }
        }
    }
    if (showDialog) {
        DateDialog(
            initial = date,
            minDate = dateRange.first,
            maxDate = dateRange.second,
            onDismiss = { showDialog = false },
            onPick = {
                onPick(it)
                showDialog = false
            },
        )
    }
}

@Composable
private fun DateDialog(
    initial: Date?,
    minDate: Date?,
    maxDate: Date?,
    onDismiss: () -> Unit,
    onPick: (Date) -> Unit,
) {
    val state = rememberDatePickerState(
        initialSelectedDateMillis = (initial ?: Date()).time,
        yearRange = run {
            val cal = Calendar.getInstance()
            val currentYear = cal.get(Calendar.YEAR)
            val minYear = minDate?.let { cal.time = it; cal.get(Calendar.YEAR) } ?: (currentYear - 5)
            val maxYear = maxDate?.let { cal.time = it; cal.get(Calendar.YEAR) } ?: currentYear
            minYear..maxYear
        },
    )
    DatePickerDialog(
        onDismissRequest = onDismiss,
        confirmButton = {
            Button(
                onClick = {
                    val millis = state.selectedDateMillis ?: return@Button
                    onPick(Date(millis))
                },
                colors = ButtonDefaults.buttonColors(containerColor = RdioPalette.Accent),
            ) { Text("OK") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("Cancel", color = RdioPalette.TextMuted) }
        },
        colors = androidx.compose.material3.DatePickerDefaults.colors(
            containerColor = RdioPalette.BgElevated,
        ),
    ) {
        DatePicker(
            state = state,
            showModeToggle = true,
            colors = androidx.compose.material3.DatePickerDefaults.colors(
                containerColor = RdioPalette.BgElevated,
                titleContentColor = RdioPalette.TextMain,
                headlineContentColor = RdioPalette.TextMain,
                weekdayContentColor = RdioPalette.TextMuted,
                subheadContentColor = RdioPalette.TextMuted,
                yearContentColor = RdioPalette.TextMain,
                currentYearContentColor = RdioPalette.Accent,
                selectedYearContentColor = Color.White,
                selectedYearContainerColor = RdioPalette.Accent,
                dayContentColor = RdioPalette.TextMain,
                selectedDayContentColor = Color.White,
                selectedDayContainerColor = RdioPalette.Accent,
                todayContentColor = RdioPalette.Accent,
                todayDateBorderColor = RdioPalette.Accent,
            ),
        )
    }
}

@Composable
private fun <T> EnumDropdown(
    label: String,
    options: List<T>,
    renderOption: (T) -> String,
    selected: T,
    enabled: Boolean = true,
    onSelect: (T) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    Box {
        PillButton(
            label = label,
            onClick = { if (enabled) expanded = true },
            enabled = enabled,
        )
        DropdownMenu(
            expanded = expanded,
            onDismissRequest = { expanded = false },
            modifier = Modifier
                .background(RdioPalette.BgElevated)
                .widthIn(min = 220.dp)
                .heightIn(max = 420.dp),
        ) {
            options.forEach { opt ->
                DropdownMenuItem(
                    text = {
                        Text(
                            renderOption(opt),
                            color = if (opt == selected) RdioPalette.Accent else RdioPalette.TextMain,
                            fontWeight = if (opt == selected) FontWeight.SemiBold else FontWeight.Normal,
                        )
                    },
                    onClick = { onSelect(opt); expanded = false },
                )
            }
        }
    }
}

@Composable
private fun PillButton(
    label: String,
    onClick: () -> Unit,
    leading: androidx.compose.ui.graphics.vector.ImageVector? = null,
    enabled: Boolean = true,
) {
    val bg = if (enabled) RdioPalette.Surface else RdioPalette.BgElevatedSoft
    val border = if (enabled) RdioPalette.BorderSubtle else RdioPalette.BorderSubtleSoft
    val fg = if (enabled) RdioPalette.TextMain else RdioPalette.TextSoft
    Row(
        modifier = Modifier
            .clip(RoundedCornerShape(999.dp))
            .background(bg, RoundedCornerShape(999.dp))
            .border(1.dp, border, RoundedCornerShape(999.dp))
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (leading != null) {
            Icon(leading, contentDescription = null, tint = fg, modifier = Modifier.size(14.dp))
        }
        Text(
            label,
            color = fg,
            style = LocalTextStyle.current.copy(fontSize = 12.sp, fontWeight = FontWeight.SemiBold),
        )
    }
}

@Composable
private fun ResultRow(
    call: SearchResultCall,
    systems: List<SystemDto>,
    onPlay: () -> Unit,
    onDownload: () -> Unit,
) {
    val system = remember(systems, call.system) { systems.firstOrNull { it.id == call.system } }
    val tg = remember(system, call.talkgroup) { system?.talkgroups?.firstOrNull { it.id == call.talkgroup } }
    val when_ = remember(call.dateTime) { parseAndFormat(call.dateTime) }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(RdioPalette.BgElevatedSoft, RoundedCornerShape(10.dp))
            .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(10.dp))
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                tg?.let { talkgroupDisplay(it) } ?: "TG ${call.talkgroup}",
                color = RdioPalette.TextMain,
                style = LocalTextStyle.current.copy(fontSize = 14.sp, fontWeight = FontWeight.SemiBold),
            )
            val subtitleParts = buildList {
                add(system?.label?.ifBlank { null } ?: "System ${call.system}")
                tg?.label?.ifBlank { null }?.takeIf { it != tg.name.ifBlank { null } }?.let { add(it) }
                add(when_)
            }
            Text(
                subtitleParts.joinToString(" · "),
                color = RdioPalette.TextMuted,
                style = LocalTextStyle.current.copy(fontSize = 12.sp),
            )
        }
        IconButton(onClick = onPlay) {
            Icon(Icons.Default.PlayArrow, contentDescription = "Play", tint = RdioPalette.Accent)
        }
        IconButton(onClick = onDownload) {
            Icon(Icons.Default.Download, contentDescription = "Download", tint = RdioPalette.TextMain)
        }
    }
}

/**
 * Prefer the descriptive talkgroup name on the Search screen — matches what
 * a user is likely scanning for. Falls back to the short label, then the id.
 */
private fun talkgroupDisplay(tg: solutions.saubeo.rdioscanner.data.protocol.TalkgroupDto): String =
    tg.name.ifBlank { null } ?: tg.label.ifBlank { null } ?: "TG ${tg.id}"

private fun parseIso(iso: String): Date? = runCatching {
    SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ssXXX", Locale.US).parse(iso)
}.getOrNull() ?: runCatching {
    SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", Locale.US).parse(iso)
}.getOrNull()

private fun parseAndFormat(iso: String): String {
    val parsed = parseIso(iso) ?: return iso
    return SimpleDateFormat("MMM d  HH:mm:ss", Locale.getDefault()).format(parsed)
}

private fun formatRfc3339(date: Date): String {
    val fmt = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", Locale.US)
    fmt.timeZone = TimeZone.getTimeZone("UTC")
    return fmt.format(date)
}


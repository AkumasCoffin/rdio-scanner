@file:OptIn(
    androidx.compose.material3.ExperimentalMaterial3Api::class,
    androidx.compose.foundation.layout.ExperimentalLayoutApi::class,
)

package solutions.saubeo.rdioscanner.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.TriStateCheckbox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import solutions.saubeo.rdioscanner.data.prefs.PresetDto
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.ui.ScannerViewModel
import solutions.saubeo.rdioscanner.ui.components.RdioButton
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette

@Composable
fun SelectorScreen(
    vm: ScannerViewModel,
    onBack: () -> Unit,
) {
    val config by vm.config.collectAsStateWithLifecycle()
    val selection by vm.selection.collectAsStateWithLifecycle()
    val presets by vm.presets.collectAsStateWithLifecycle()

    val expanded = remember { mutableStateMapOf<Int, Boolean>() }
    var dialogPreset by remember { mutableStateOf<PresetDto?>(null) }
    var dialogOpen by remember { mutableStateOf(false) }

    Scaffold(
        containerColor = Color.Transparent,
        topBar = {
            TopAppBar(
                title = { Text("Select Talkgroups") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = null, tint = RdioPalette.TextMain)
                    }
                },
                actions = {
                    TextButton(onClick = { vm.setAll(true) }) { Text("All", color = RdioPalette.Accent) }
                    TextButton(onClick = { vm.setAll(false) }) { Text("None", color = RdioPalette.Accent) }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = Color.Transparent,
                    titleContentColor = RdioPalette.TextMain,
                ),
            )
        },
    ) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
            contentPadding = androidx.compose.foundation.layout.PaddingValues(vertical = 12.dp),
        ) {
            item {
                PresetsSection(
                    presets = presets,
                    onApply = vm::applyPreset,
                    onEdit = {
                        dialogPreset = it
                        dialogOpen = true
                    },
                    onDelete = vm::deletePreset,
                    onCreate = {
                        dialogPreset = null
                        dialogOpen = true
                    },
                )
            }
            val systems = config?.systems.orEmpty()
            if (systems.isEmpty()) {
                item {
                    CardPanel { Text("No systems in config yet", color = RdioPalette.TextMuted) }
                }
            } else {
                items(systems, key = { it.id }) { system ->
                    SystemCard(
                        system = system,
                        selection = selection,
                        expanded = expanded[system.id] == true,
                        onExpand = { expanded[system.id] = !(expanded[system.id] == true) },
                        onSystemToggle = { active ->
                            vm.toggleSystem(system.id, system.talkgroups.map { it.id }, active)
                        },
                        onTalkgroupToggle = { tgId, active ->
                            vm.toggleTalkgroup(system.id, tgId, active)
                        },
                    )
                }
            }
        }
    }

    if (dialogOpen) {
        PresetEditor(
            existing = dialogPreset,
            systems = config?.systems.orEmpty(),
            initialSelection = dialogPreset?.let { preset ->
                config?.systems?.associate { sys ->
                    val ids = preset.talkgroups[sys.id]?.toSet().orEmpty()
                    sys.id to sys.talkgroups.associate { tg -> tg.id to (tg.id in ids) }
                } ?: emptyMap()
            } ?: selection,
            onCancel = { dialogOpen = false },
            onSave = { name, map ->
                vm.savePreset(name, map, editing = dialogPreset)
                dialogOpen = false
            },
        )
    }
}

@Composable
private fun PresetsSection(
    presets: List<PresetDto>,
    onApply: (PresetDto) -> Unit,
    onEdit: (PresetDto) -> Unit,
    onDelete: (String) -> Unit,
    onCreate: () -> Unit,
) {
    CardPanel {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                "Presets".uppercase(),
                style = LocalTextStyle.current.copy(
                    fontSize = 12.sp,
                    fontWeight = FontWeight.SemiBold,
                    letterSpacing = 1.5.sp,
                ),
                color = RdioPalette.TextSoft,
                modifier = Modifier.weight(1f),
            )
            RdioButton(
                label = "NEW",
                onClick = onCreate,
                modifier = Modifier.height(40.dp),
            )
        }
        Spacer(Modifier.height(10.dp))
        if (presets.isEmpty()) {
            Text(
                "No presets yet. Save your current selection as a preset to recall it later.",
                color = RdioPalette.TextMuted,
                style = LocalTextStyle.current.copy(fontSize = 13.sp),
            )
            return@CardPanel
        }
        presets.forEach { preset ->
            PresetRow(preset = preset, onApply = onApply, onEdit = onEdit, onDelete = onDelete)
            Spacer(Modifier.height(6.dp))
        }
    }
}

@Composable
private fun PresetRow(
    preset: PresetDto,
    onApply: (PresetDto) -> Unit,
    onEdit: (PresetDto) -> Unit,
    onDelete: (String) -> Unit,
) {
    val count = preset.talkgroups.values.sumOf { it.size }
    var confirmDelete by remember { mutableStateOf(false) }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(RdioPalette.BgElevatedSoft, RoundedCornerShape(10.dp))
            .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(10.dp))
            .padding(horizontal = 10.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(preset.name, color = RdioPalette.TextMain, fontWeight = FontWeight.SemiBold)
            Text(
                "$count talkgroup${if (count == 1) "" else "s"}",
                color = RdioPalette.TextSoft,
                style = LocalTextStyle.current.copy(fontSize = 11.sp),
            )
        }
        TextButton(onClick = { onApply(preset) }) { Text("APPLY", color = RdioPalette.Accent) }
        TextButton(onClick = { onEdit(preset) }) { Text("EDIT", color = RdioPalette.TextMuted) }
        IconButton(onClick = { confirmDelete = true }) {
            Icon(Icons.Default.Close, contentDescription = "Delete", tint = RdioPalette.Red)
        }
    }
    if (confirmDelete) {
        AlertDialog(
            onDismissRequest = { confirmDelete = false },
            confirmButton = {
                TextButton(onClick = { onDelete(preset.id); confirmDelete = false }) {
                    Text("Delete", color = RdioPalette.Red)
                }
            },
            dismissButton = {
                TextButton(onClick = { confirmDelete = false }) {
                    Text("Cancel", color = RdioPalette.TextMuted)
                }
            },
            title = { Text("Delete preset?") },
            text = { Text("\"${preset.name}\" will be removed.") },
            containerColor = RdioPalette.BgElevated,
            titleContentColor = RdioPalette.TextMain,
            textContentColor = RdioPalette.TextMuted,
        )
    }
}

@Composable
private fun SystemCard(
    system: SystemDto,
    selection: Map<Int, Map<Int, Boolean>>,
    expanded: Boolean,
    onExpand: () -> Unit,
    onSystemToggle: (Boolean) -> Unit,
    onTalkgroupToggle: (Int, Boolean) -> Unit,
) {
    val inner = selection[system.id].orEmpty()
    val activeCount = system.talkgroups.count { inner[it.id] == true }
    val tri = when {
        activeCount == 0 -> ToggleableState.Off
        activeCount == system.talkgroups.size -> ToggleableState.On
        else -> ToggleableState.Indeterminate
    }
    CardPanel {
        Row(
            Modifier.fillMaxWidth().clickable(onClick = onExpand),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            TriStateCheckbox(
                state = tri,
                onClick = { onSystemToggle(tri != ToggleableState.On) },
                colors = checkboxColors(),
            )
            Spacer(Modifier.size(4.dp))
            Column(Modifier.weight(1f)) {
                Text(system.label, color = RdioPalette.TextMain, fontWeight = FontWeight.SemiBold)
                Text(
                    "$activeCount / ${system.talkgroups.size} active",
                    color = RdioPalette.TextSoft,
                    style = LocalTextStyle.current.copy(fontSize = 11.sp),
                )
            }
            Icon(
                if (expanded) Icons.Default.ExpandLess else Icons.Default.ExpandMore,
                contentDescription = null,
                tint = RdioPalette.TextMuted,
            )
        }
        if (expanded) {
            Spacer(Modifier.height(4.dp))
            system.talkgroups.forEach { tg ->
                Row(
                    Modifier.fillMaxWidth().padding(start = 32.dp, top = 2.dp, bottom = 2.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    val active = inner[tg.id] == true
                    Checkbox(
                        checked = active,
                        onCheckedChange = { onTalkgroupToggle(tg.id, it) },
                        colors = checkboxColors(),
                    )
                    Spacer(Modifier.size(4.dp))
                    Column(Modifier.weight(1f)) {
                        Text(
                            tg.label.ifBlank { tg.name.ifBlank { "TG ${tg.id}" } },
                            color = RdioPalette.TextMain,
                        )
                        val sub = listOfNotNull(
                            tg.tag.takeIf { it.isNotBlank() },
                            tg.group.takeIf { it.isNotBlank() },
                        ).joinToString(" · ")
                        if (sub.isNotBlank()) {
                            Text(
                                sub,
                                color = RdioPalette.TextSoft,
                                style = LocalTextStyle.current.copy(fontSize = 11.sp),
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun PresetEditor(
    existing: PresetDto?,
    systems: List<SystemDto>,
    initialSelection: Map<Int, Map<Int, Boolean>>,
    onCancel: () -> Unit,
    onSave: (String, Map<Int, Map<Int, Boolean>>) -> Unit,
) {
    var name by remember { mutableStateOf(existing?.name.orEmpty()) }
    val localSelection = remember {
        mutableStateMapOf<Int, MutableMap<Int, Boolean>>().apply {
            initialSelection.forEach { (k, v) -> put(k, v.toMutableMap()) }
        }
    }

    fun toggleTg(sys: Int, tg: Int, active: Boolean) {
        val inner = localSelection[sys]?.toMutableMap() ?: mutableMapOf()
        inner[tg] = active
        localSelection[sys] = inner
    }

    fun toggleSys(sys: SystemDto, active: Boolean) {
        localSelection[sys.id] = sys.talkgroups.associate { it.id to active }.toMutableMap()
    }

    AlertDialog(
        onDismissRequest = onCancel,
        confirmButton = {
            TextButton(
                onClick = {
                    val map = localSelection.mapValues { (_, v) -> v.toMap() }
                    onSave(name, map)
                },
                enabled = name.isNotBlank(),
            ) {
                Text(if (existing != null) "Update" else "Create", color = RdioPalette.Accent)
            }
        },
        dismissButton = {
            TextButton(onClick = onCancel) { Text("Cancel", color = RdioPalette.TextMuted) }
        },
        title = { Text(if (existing != null) "Edit Preset" else "Create Preset") },
        text = {
            Column(modifier = Modifier.fillMaxWidth()) {
                Text("Preset name", color = RdioPalette.TextSoft, style = LocalTextStyle.current.copy(fontSize = 11.sp))
                Spacer(Modifier.height(4.dp))
                Box(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(RdioPalette.Surface, RoundedCornerShape(8.dp))
                        .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(8.dp))
                        .padding(horizontal = 10.dp, vertical = 8.dp),
                ) {
                    BasicTextField(
                        value = name,
                        onValueChange = { name = it },
                        singleLine = true,
                        textStyle = LocalTextStyle.current.copy(color = RdioPalette.TextMain),
                        cursorBrush = androidx.compose.ui.graphics.SolidColor(RdioPalette.Accent),
                    )
                    if (name.isBlank()) {
                        Text(
                            "Enter preset name",
                            color = RdioPalette.TextSoft,
                            style = LocalTextStyle.current,
                        )
                    }
                }
                Spacer(Modifier.height(12.dp))
                Text(
                    "Talkgroups",
                    color = RdioPalette.TextSoft,
                    style = LocalTextStyle.current.copy(fontSize = 11.sp, letterSpacing = 1.sp),
                )
                Spacer(Modifier.height(4.dp))
                // Light embedded list (scrolls inside dialog)
                LazyColumn(
                    modifier = Modifier.fillMaxWidth().height(280.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    items(systems, key = { it.id }) { sys ->
                        val activeCount = sys.talkgroups.count { localSelection[sys.id]?.get(it.id) == true }
                        val tri = when {
                            activeCount == 0 -> ToggleableState.Off
                            activeCount == sys.talkgroups.size -> ToggleableState.On
                            else -> ToggleableState.Indeterminate
                        }
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            TriStateCheckbox(
                                state = tri,
                                onClick = { toggleSys(sys, tri != ToggleableState.On) },
                                colors = checkboxColors(),
                            )
                            Text(
                                sys.label,
                                color = RdioPalette.TextMain,
                                fontWeight = FontWeight.SemiBold,
                            )
                        }
                        // tag chips row
                        androidx.compose.foundation.layout.FlowRow(
                            modifier = Modifier.fillMaxWidth().padding(start = 28.dp, bottom = 2.dp),
                            horizontalArrangement = Arrangement.spacedBy(6.dp),
                            verticalArrangement = Arrangement.spacedBy(4.dp),
                        ) {
                            sys.talkgroups.forEach { tg ->
                                val active = localSelection[sys.id]?.get(tg.id) == true
                                TalkgroupChip(
                                    label = tg.label.ifBlank { "TG ${tg.id}" },
                                    active = active,
                                    onClick = { toggleTg(sys.id, tg.id, !active) },
                                )
                            }
                        }
                    }
                }
            }
        },
        containerColor = RdioPalette.BgElevated,
        titleContentColor = RdioPalette.TextMain,
        textContentColor = RdioPalette.TextMain,
    )
}

@Composable
private fun TalkgroupChip(label: String, active: Boolean, onClick: () -> Unit) {
    val bg = if (active) Color(0x4022C55E) else RdioPalette.Surface
    val border = if (active) Color(0x8022C55E) else RdioPalette.BorderSubtle
    val text = if (active) Color(0xFFBBF7D0) else RdioPalette.TextMuted
    Box(
        Modifier
            .clip(RoundedCornerShape(999.dp))
            .background(bg, RoundedCornerShape(999.dp))
            .border(1.dp, border, RoundedCornerShape(999.dp))
            .clickable(onClick = onClick)
            .padding(horizontal = 10.dp, vertical = 4.dp),
    ) {
        Text(
            label,
            color = text,
            style = LocalTextStyle.current.copy(fontSize = 12.sp, fontWeight = FontWeight.SemiBold),
        )
    }
}

@Composable
private fun CardPanel(content: @Composable () -> Unit) {
    Column(
        Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .background(RdioPalette.BgElevated, RoundedCornerShape(14.dp))
            .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(14.dp))
            .padding(horizontal = 14.dp, vertical = 12.dp),
    ) { content() }
}

@Composable
private fun checkboxColors() = CheckboxDefaults.colors(
    checkedColor = RdioPalette.Accent,
    uncheckedColor = RdioPalette.TextSoft,
    checkmarkColor = Color.White,
)

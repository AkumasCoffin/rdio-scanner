@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Lock
import androidx.compose.material.icons.filled.Router
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalTextStyle
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.prefs.ConnectionProfileDto
import solutions.saubeo.rdioscanner.ui.ScannerViewModel
import solutions.saubeo.rdioscanner.ui.components.RdioButton
import solutions.saubeo.rdioscanner.ui.components.RdioClickTone
import solutions.saubeo.rdioscanner.ui.theme.RdioPalette

@Composable
fun ConnectScreen(vm: ScannerViewModel) {
    val state by vm.state.collectAsStateWithLifecycle()
    val profiles by vm.profiles.collectAsStateWithLifecycle()
    val lastId by vm.lastProfileId.collectAsStateWithLifecycle()

    var editorOpen by remember { mutableStateOf(false) }
    var editing by remember { mutableStateOf<ConnectionProfileDto?>(null) }
    var confirmDelete by remember { mutableStateOf<ConnectionProfileDto?>(null) }

    val openAdd = {
        editing = null
        editorOpen = true
    }
    val openEdit: (ConnectionProfileDto) -> Unit = { p ->
        editing = p
        editorOpen = true
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .widthIn(max = 640.dp)
            .padding(horizontal = 20.dp, vertical = 24.dp),
    ) {
        Text(
            text = "RDIO SCANNER",
            style = TextStyle(
                fontSize = 22.sp,
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 3.sp,
                color = RdioPalette.TextMain,
            ),
        )
        Spacer(Modifier.height(4.dp))
        Text(
            text = "Connections",
            color = RdioPalette.TextSoft,
            style = LocalTextStyle.current.copy(
                fontSize = 11.sp,
                letterSpacing = 2.sp,
                fontWeight = FontWeight.SemiBold,
            ),
        )
        Spacer(Modifier.height(16.dp))

        if (profiles.isEmpty()) {
            EmptyProfiles(onAdd = openAdd)
        } else {
            LazyColumn(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                items(profiles, key = { it.id }) { profile ->
                    ProfileRow(
                        profile = profile,
                        isActive = profile.id == lastId && state == ConnectionState.Connected,
                        isConnecting = profile.id == lastId &&
                            (state == ConnectionState.Connecting || state == ConnectionState.Authenticating),
                        onConnect = { vm.connectProfile(profile) },
                        onEdit = { openEdit(profile) },
                        onDelete = { confirmDelete = profile },
                    )
                }
            }
            Spacer(Modifier.height(16.dp))
        }

        RdioButton(
            label = "ADD CONNECTION",
            onClick = openAdd,
            modifier = Modifier.fillMaxWidth().height(52.dp),
            tone = RdioClickTone.Activate,
        )
        Spacer(Modifier.height(14.dp))
        StatusText(state = state)
    }

    if (editorOpen) {
        ProfileEditor(
            existing = editing,
            onCancel = { editorOpen = false },
            onSave = { name, url, code ->
                vm.saveProfile(name, url, code, editing = editing)
                editorOpen = false
            },
        )
    }

    confirmDelete?.let { profile ->
        AlertDialog(
            onDismissRequest = { confirmDelete = null },
            confirmButton = {
                TextButton(onClick = { vm.deleteProfile(profile.id); confirmDelete = null }) {
                    Text("Delete", color = RdioPalette.Red)
                }
            },
            dismissButton = {
                TextButton(onClick = { confirmDelete = null }) {
                    Text("Cancel", color = RdioPalette.TextMuted)
                }
            },
            title = { Text("Delete connection?") },
            text = { Text("\"${profile.name}\" will be removed from this device.") },
            containerColor = RdioPalette.BgElevated,
            titleContentColor = RdioPalette.TextMain,
            textContentColor = RdioPalette.TextMuted,
        )
    }
}

@Composable
private fun EmptyProfiles(onAdd: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(14.dp))
            .background(RdioPalette.BgElevated, RoundedCornerShape(14.dp))
            .border(1.dp, RdioPalette.BorderSubtle, RoundedCornerShape(14.dp))
            .padding(horizontal = 20.dp, vertical = 28.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Icon(
            Icons.Default.Router,
            contentDescription = null,
            tint = RdioPalette.TextMuted,
            modifier = Modifier.size(28.dp),
        )
        Spacer(Modifier.height(10.dp))
        Text("No connections yet", color = RdioPalette.TextMain, fontWeight = FontWeight.SemiBold)
        Spacer(Modifier.height(6.dp))
        Text(
            "Add your first Rdio Scanner server to get started.",
            color = RdioPalette.TextMuted,
            style = LocalTextStyle.current.copy(fontSize = 13.sp),
        )
    }
}

@Composable
private fun ProfileRow(
    profile: ConnectionProfileDto,
    isActive: Boolean,
    isConnecting: Boolean,
    onConnect: () -> Unit,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
) {
    val accentBorder = if (isActive) RdioPalette.Green else if (isConnecting) RdioPalette.Yellow else RdioPalette.BorderSubtle
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(12.dp))
            .background(RdioPalette.BgElevated, RoundedCornerShape(12.dp))
            .border(1.dp, accentBorder, RoundedCornerShape(12.dp))
            .clickable(enabled = !isActive, onClick = onConnect)
            .padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        profile.name,
                        color = RdioPalette.TextMain,
                        fontWeight = FontWeight.SemiBold,
                        style = LocalTextStyle.current.copy(fontSize = 15.sp),
                    )
                    if (isActive) {
                        Spacer(Modifier.size(8.dp))
                        Pill(label = "CONNECTED", color = RdioPalette.Green)
                    } else if (isConnecting) {
                        Spacer(Modifier.size(8.dp))
                        Pill(label = "CONNECTING", color = RdioPalette.Yellow)
                    }
                }
                Spacer(Modifier.height(2.dp))
                Text(
                    profile.serverUrl,
                    color = RdioPalette.TextMuted,
                    style = LocalTextStyle.current.copy(fontSize = 12.sp),
                )
                if (profile.accessCode.isNotBlank()) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Icon(
                            Icons.Default.Lock,
                            contentDescription = null,
                            tint = RdioPalette.TextSoft,
                            modifier = Modifier.size(10.dp),
                        )
                        Spacer(Modifier.size(4.dp))
                        Text(
                            "access code saved",
                            color = RdioPalette.TextSoft,
                            style = LocalTextStyle.current.copy(fontSize = 11.sp),
                        )
                    }
                }
            }
            IconButton(onClick = onEdit) {
                Icon(Icons.Default.Edit, contentDescription = "Edit", tint = RdioPalette.TextMuted)
            }
            IconButton(onClick = onDelete) {
                Icon(Icons.Default.Close, contentDescription = "Delete", tint = RdioPalette.Red)
            }
        }
        if (!isActive) {
            Spacer(Modifier.height(10.dp))
            RdioButton(
                label = if (isConnecting) "CONNECTING…" else "CONNECT",
                onClick = onConnect,
                modifier = Modifier.fillMaxWidth().height(44.dp),
                tone = RdioClickTone.Activate,
                enabled = !isConnecting,
            )
        }
    }
}

@Composable
private fun Pill(label: String, color: Color) {
    Box(
        Modifier
            .clip(RoundedCornerShape(999.dp))
            .background(color.copy(alpha = 0.15f), RoundedCornerShape(999.dp))
            .border(1.dp, color.copy(alpha = 0.5f), RoundedCornerShape(999.dp))
            .padding(horizontal = 8.dp, vertical = 2.dp),
    ) {
        Text(
            label,
            color = color,
            style = LocalTextStyle.current.copy(
                fontSize = 9.sp,
                letterSpacing = 1.sp,
                fontWeight = FontWeight.SemiBold,
            ),
        )
    }
}

@Composable
private fun ProfileEditor(
    existing: ConnectionProfileDto?,
    onCancel: () -> Unit,
    onSave: (name: String, url: String, code: String) -> Unit,
) {
    var name by remember { mutableStateOf(existing?.name.orEmpty()) }
    var url by remember { mutableStateOf(existing?.serverUrl.orEmpty()) }
    var code by remember { mutableStateOf(existing?.accessCode.orEmpty()) }

    AlertDialog(
        onDismissRequest = onCancel,
        title = { Text(if (existing != null) "Edit connection" else "Add connection") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                DialogField(
                    label = "NAME",
                    value = name,
                    onChange = { name = it },
                    placeholder = "Home server",
                    keyboardType = KeyboardType.Text,
                )
                DialogField(
                    label = "SERVER URL",
                    value = url,
                    onChange = { url = it },
                    placeholder = "https://server.example.com",
                    keyboardType = KeyboardType.Uri,
                )
                DialogField(
                    label = "ACCESS CODE",
                    value = code,
                    onChange = { code = it },
                    placeholder = "optional",
                    keyboardType = KeyboardType.Password,
                    visualTransformation = PasswordVisualTransformation(),
                )
            }
        },
        confirmButton = {
            Button(
                onClick = { onSave(name, url, code) },
                enabled = name.isNotBlank() && url.isNotBlank(),
                colors = ButtonDefaults.buttonColors(containerColor = RdioPalette.Accent),
            ) { Text(if (existing != null) "Save" else "Add") }
        },
        dismissButton = {
            TextButton(onClick = onCancel) { Text("Cancel", color = RdioPalette.TextMuted) }
        },
        containerColor = RdioPalette.BgElevated,
        titleContentColor = RdioPalette.TextMain,
        textContentColor = RdioPalette.TextMain,
    )
}

@Composable
private fun DialogField(
    label: String,
    value: String,
    onChange: (String) -> Unit,
    placeholder: String,
    keyboardType: KeyboardType,
    visualTransformation: VisualTransformation = VisualTransformation.None,
) {
    Column(Modifier.fillMaxWidth()) {
        Text(
            text = label,
            color = RdioPalette.TextSoft,
            style = TextStyle(fontSize = 10.sp, fontWeight = FontWeight.SemiBold, letterSpacing = 1.5.sp),
        )
        Spacer(Modifier.height(4.dp))
        val shape = RoundedCornerShape(8.dp)
        Box(
            Modifier
                .fillMaxWidth()
                .clip(shape)
                .background(RdioPalette.Surface, shape)
                .border(1.dp, RdioPalette.BorderSubtle, shape)
                .padding(horizontal = 10.dp, vertical = 8.dp),
        ) {
            if (value.isEmpty()) {
                Text(
                    text = placeholder,
                    color = RdioPalette.TextSoft,
                    style = TextStyle(fontSize = 13.sp),
                )
            }
            BasicTextField(
                value = value,
                onValueChange = onChange,
                singleLine = true,
                textStyle = TextStyle(color = RdioPalette.TextMain, fontSize = 13.sp),
                cursorBrush = SolidColor(RdioPalette.Accent),
                visualTransformation = visualTransformation,
                keyboardOptions = KeyboardOptions(keyboardType = keyboardType),
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

@Composable
private fun StatusText(state: ConnectionState) {
    val (label, color) = when (state) {
        ConnectionState.Disconnected -> "DISCONNECTED" to RdioPalette.TextSoft
        ConnectionState.Connecting -> "CONNECTING…" to RdioPalette.TextMuted
        ConnectionState.Authenticating -> "AUTHENTICATING…" to RdioPalette.TextMuted
        ConnectionState.Connected -> "CONNECTED" to RdioPalette.Green
        ConnectionState.AuthFailed -> "ACCESS DENIED" to RdioPalette.Red
        ConnectionState.Expired -> "ACCESS EXPIRED" to RdioPalette.Red
        ConnectionState.TooMany -> "TOO MANY CONNECTIONS" to RdioPalette.Yellow
        is ConnectionState.Error -> state.message.uppercase() to RdioPalette.Red
    }
    Text(
        text = label,
        color = color,
        style = TextStyle(fontSize = 12.sp, letterSpacing = 1.5.sp, fontWeight = FontWeight.SemiBold),
    )
}


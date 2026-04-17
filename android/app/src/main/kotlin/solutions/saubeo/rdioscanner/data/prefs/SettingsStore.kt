package solutions.saubeo.rdioscanner.data.prefs

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore(name = "rdio-settings")

@Serializable
data class SelectionState(
    val map: Map<Int, Map<Int, Boolean>> = emptyMap(),
)

@Serializable
data class PresetDto(
    val id: String,
    val name: String,
    /** systemId -> list of talkgroupIds */
    val talkgroups: Map<Int, List<Int>>,
    val createdAt: Long = System.currentTimeMillis(),
)

@Serializable
data class PresetBundle(
    val version: String = "1",
    val presets: List<PresetDto> = emptyList(),
    val exportedAt: Long = System.currentTimeMillis(),
)

@Serializable
data class ConnectionProfileDto(
    val id: String,
    val name: String,
    val serverUrl: String,
    val accessCode: String = "",
    val createdAt: Long = System.currentTimeMillis(),
)

@Serializable
data class ConnectionProfileBundle(
    val version: String = "1",
    val profiles: List<ConnectionProfileDto> = emptyList(),
)

class SettingsStore(private val context: Context) {
    private val keyServerUrl = stringPreferencesKey("server_url")
    private val keyAccessCode = stringPreferencesKey("access_code")
    private val keySelection = stringPreferencesKey("selection_json")
    private val keySelectionInit = booleanPreferencesKey("selection_initialized")
    private val keyPresets = stringPreferencesKey("presets_json")
    private val keyProfiles = stringPreferencesKey("profiles_json")
    private val keyLastProfileId = stringPreferencesKey("last_profile_id")

    val serverUrl: Flow<String> = context.dataStore.data.map { it[keyServerUrl].orEmpty() }
    val accessCode: Flow<String> = context.dataStore.data.map { it[keyAccessCode].orEmpty() }

    val selection: Flow<Map<Int, Map<Int, Boolean>>> = context.dataStore.data.map { prefs ->
        val raw = prefs[keySelection] ?: return@map emptyMap()
        runCatching {
            json.decodeFromString(SelectionState.serializer(), raw).map
        }.getOrDefault(emptyMap())
    }

    val selectionInitialized: Flow<Boolean> =
        context.dataStore.data.map { it[keySelectionInit] == true }

    val presets: Flow<List<PresetDto>> = context.dataStore.data.map { prefs ->
        val raw = prefs[keyPresets] ?: return@map emptyList()
        runCatching {
            json.decodeFromString(PresetBundle.serializer(), raw).presets
        }.getOrDefault(emptyList())
    }

    val profiles: Flow<List<ConnectionProfileDto>> = context.dataStore.data.map { prefs ->
        val raw = prefs[keyProfiles] ?: return@map emptyList()
        runCatching {
            json.decodeFromString(ConnectionProfileBundle.serializer(), raw).profiles
        }.getOrDefault(emptyList())
    }

    val lastProfileId: Flow<String?> = context.dataStore.data.map {
        it[keyLastProfileId]?.ifBlank { null }
    }

    suspend fun setServer(url: String, accessCode: String) {
        context.dataStore.edit { prefs ->
            prefs[keyServerUrl] = url
            prefs[keyAccessCode] = accessCode
        }
    }

    /**
     * Atomic write of the credentials + last-used profile id so a process
     * death between two separate edits can't leave lastProfileId pointing
     * at stale serverUrl / accessCode values.
     */
    suspend fun setActiveProfile(url: String, accessCode: String, profileId: String) {
        context.dataStore.edit { prefs ->
            prefs[keyServerUrl] = url
            prefs[keyAccessCode] = accessCode
            prefs[keyLastProfileId] = profileId
        }
    }

    suspend fun setSelection(map: Map<Int, Map<Int, Boolean>>, markInitialized: Boolean = true) {
        val encoded = json.encodeToString(SelectionState.serializer(), SelectionState(map))
        context.dataStore.edit { prefs ->
            prefs[keySelection] = encoded
            if (markInitialized) prefs[keySelectionInit] = true
        }
    }

    suspend fun savePresets(list: List<PresetDto>) {
        val encoded = json.encodeToString(PresetBundle.serializer(), PresetBundle(presets = list))
        context.dataStore.edit { prefs -> prefs[keyPresets] = encoded }
    }

    suspend fun currentPresets(): List<PresetDto> = presets.first()

    suspend fun saveProfiles(list: List<ConnectionProfileDto>) {
        val encoded = json.encodeToString(
            ConnectionProfileBundle.serializer(),
            ConnectionProfileBundle(profiles = list),
        )
        context.dataStore.edit { prefs -> prefs[keyProfiles] = encoded }
    }

    suspend fun currentProfiles(): List<ConnectionProfileDto> = profiles.first()

    suspend fun setLastProfileId(id: String?) {
        context.dataStore.edit { prefs ->
            if (id == null) prefs.remove(keyLastProfileId) else prefs[keyLastProfileId] = id
        }
    }

    suspend fun clearCredentials() {
        context.dataStore.edit { prefs ->
            prefs.remove(keyServerUrl)
            prefs.remove(keyAccessCode)
        }
    }

    companion object {
        val json = Json {
            ignoreUnknownKeys = true
            encodeDefaults = true
        }
    }
}

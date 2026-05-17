package solutions.saubeo.rdioscanner.data.protocol

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull

/** Command constants (mirrors server/message.go). */
object Cmd {
    const val CAL = "CAL"
    const val CFG = "CFG"
    const val XPR = "XPR"
    const val LCL = "LCL"
    const val LSC = "LSC"
    const val LFM = "LFM"
    const val MAX = "MAX"
    const val PIN = "PIN"
    const val TRX = "TRX"
    const val VER = "VER"
    const val SRV = "SRV"
}

/** Server → client message, in parsed form. */
sealed interface Incoming {
    data class Version(val payload: ServerVersion) : Incoming
    data class Config(val payload: ConfigDto) : Incoming
    data class Call(val payload: CallDto, val flag: String?) : Incoming
    data class Listeners(val count: Int) : Incoming
    data class LivefeedAck(val active: Boolean) : Incoming
    data class Search(val payload: SearchResults) : Incoming
    /**
     * Whisper transcript for [callId]. Server emits this both as a
     * direct reply to a TRX request from us and as an unsolicited
     * push when a backend transcription run finishes for a call our
     * access scope can see. Same shape either way; we don't bother
     * distinguishing — the listener just merges into the cache.
     */
    data class Transcript(val callId: Long, val text: String) : Incoming
    data object PinRequested : Incoming
    data object Expired : Incoming
    data object TooMany : Incoming
    data class Unknown(val command: String, val raw: JsonElement?) : Incoming
}

object Wire {
    val json: Json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = false
        explicitNulls = false
        isLenient = true
    }

    /** Encode an outgoing array-framed message. Null payload/flag are omitted. */
    fun encode(command: String, payload: JsonElement? = null, flag: String? = null): String {
        val arr = buildJsonArray {
            add(JsonPrimitive(command))
            if (payload != null && payload !is JsonNull) add(payload)
            if (!flag.isNullOrEmpty()) add(JsonPrimitive(flag))
        }
        return json.encodeToString(JsonArray.serializer(), arr)
    }

    fun version(): String = encode(Cmd.VER)

    fun config(): String = encode(Cmd.CFG)

    fun pin(accessCode: String): String =
        encode(Cmd.PIN, JsonPrimitive(base64(accessCode)))

    fun livefeedMap(map: LivefeedMap): String {
        val payload = buildJsonObject {
            for ((sys, tgs) in map) {
                put(sys.toString(), buildJsonObject {
                    for ((tg, active) in tgs) {
                        put(tg.toString(), JsonPrimitive(active))
                    }
                })
            }
        }
        return encode(Cmd.LFM, payload)
    }

    /** Webapp's `stopLivefeed`: `["LFM"]` with no payload clears the server-side matrix. */
    fun clearLivefeed(): String = encode(Cmd.LFM)

    fun call(id: Long, flag: String? = null): String =
        encode(Cmd.CAL, JsonPrimitive(id.toString()), flag)

    fun listCall(opts: SearchOptions): String {
        val payload = json.encodeToJsonElement(SearchOptions.serializer(), opts)
        return encode(Cmd.LCL, payload)
    }

    /**
     * Request the transcript text for a given call id. Server accepts
     * either a numeric or stringified payload; we send a string to
     * mirror what `Wire.call` does (avoids precision quirks for ids
     * near the 32-bit boundary). Server replies with another TRX frame
     * whose payload object holds `{id, transcript}` — handled by the
     * Cmd.TRX branch in [decode].
     */
    fun transcript(id: Long): String =
        encode(Cmd.TRX, JsonPrimitive(id.toString()))

    fun decode(raw: String): Incoming? {
        val arr = runCatching { json.parseToJsonElement(raw) }.getOrNull()?.jsonArray
            ?: return null
        if (arr.isEmpty()) return null
        val cmd = arr[0].jsonPrimitive.contentOrNull ?: return null
        val payload: JsonElement? = arr.getOrNull(1)?.takeUnless { it is JsonNull }
        val flag: String? = arr.getOrNull(2)?.jsonPrimitive?.contentOrNull

        return when (cmd) {
            Cmd.VER -> payload?.let {
                Incoming.Version(json.decodeFromJsonElement(ServerVersion.serializer(), it))
            } ?: Incoming.Version(ServerVersion(version = ""))

            Cmd.CFG -> payload?.let {
                Incoming.Config(json.decodeFromJsonElement(ConfigDto.serializer(), it))
            } ?: Incoming.Unknown(cmd, payload)

            Cmd.CAL -> payload?.let {
                val call = json.decodeFromJsonElement(CallDto.serializer(), it)
                Incoming.Call(call, flag)
            } ?: Incoming.Unknown(cmd, payload)

            Cmd.LSC -> {
                val count = (payload as? JsonPrimitive)?.intOrNull
                    ?: (payload as? JsonPrimitive)?.longOrNull?.toInt()
                    ?: 0
                Incoming.Listeners(count)
            }

            Cmd.LFM -> {
                val active = (payload as? JsonPrimitive)?.booleanOrNull ?: false
                Incoming.LivefeedAck(active)
            }

            Cmd.LCL -> (payload as? JsonObject)?.let {
                Incoming.Search(json.decodeFromJsonElement(SearchResults.serializer(), it))
            } ?: Incoming.Unknown(cmd, payload)

            Cmd.TRX -> (payload as? JsonObject)?.let { obj ->
                // Server emits both request replies ({id, transcript}) and
                // unsolicited pushes ({id, system, talkgroup, transcript})
                // under the same TRX command. We only need id + transcript;
                // the extras get ignored.
                val id = obj["id"]?.jsonPrimitive?.longOrNull
                    ?: obj["id"]?.jsonPrimitive?.contentOrNull?.toLongOrNull()
                val text = obj["transcript"]?.jsonPrimitive?.contentOrNull
                if (id != null) {
                    Incoming.Transcript(callId = id, text = text.orEmpty())
                } else {
                    Incoming.Unknown(cmd, payload)
                }
            } ?: Incoming.Unknown(cmd, payload)

            Cmd.PIN -> Incoming.PinRequested
            Cmd.XPR -> Incoming.Expired
            Cmd.MAX -> Incoming.TooMany
            else -> Incoming.Unknown(cmd, payload)
        }
    }

    private fun base64(input: String): String =
        android.util.Base64.encodeToString(input.toByteArray(Charsets.UTF_8), android.util.Base64.NO_WRAP)
}

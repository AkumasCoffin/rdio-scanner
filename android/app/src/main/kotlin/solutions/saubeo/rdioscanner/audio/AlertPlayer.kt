package solutions.saubeo.rdioscanner.audio

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import kotlinx.coroutines.delay
import solutions.saubeo.rdioscanner.data.protocol.OscillatorBeep
import kotlin.math.PI
import kotlin.math.sin

/**
 * Synthesizes the webapp's oscillator-style alert presets (server/alert.go)
 * into PCM and plays them via AudioTrack. Each preset is a list of
 * [OscillatorBeep] entries with start/end times in seconds, a frequency in
 * Hz, and a waveform type — we build a single mono 16-bit buffer covering
 * the whole sequence (silence between beeps) and hand it to AudioTrack in
 * one write.
 */
class AlertPlayer {

    /**
     * Plays [beeps] and suspends until playback should be finished. Safe to
     * call on Dispatchers.IO. Returns immediately for empty or zero-duration
     * sequences. Caller is responsible for any pause/resume of an
     * accompanying media player around this call.
     */
    suspend fun play(beeps: List<OscillatorBeep>) {
        if (beeps.isEmpty()) return
        val maxEnd = beeps.maxOf { it.end }
        if (maxEnd <= 0f) return

        val totalSamples = (maxEnd * SAMPLE_RATE).toInt()
        if (totalSamples <= 0) return
        val pcm = ShortArray(totalSamples)

        for (beep in beeps) {
            val startSample = (beep.begin * SAMPLE_RATE).toInt().coerceAtLeast(0)
            val endSample = (beep.end * SAMPLE_RATE).toInt().coerceAtMost(totalSamples)
            if (endSample <= startSample || beep.frequency <= 0) continue
            val periodSamples = SAMPLE_RATE.toFloat() / beep.frequency
            for (i in startSample until endSample) {
                val phase = ((i - startSample).toFloat() / periodSamples) % 1f
                pcm[i] = sampleFor(beep.type, phase)
            }
        }

        val minBuf = AudioTrack.getMinBufferSize(
            SAMPLE_RATE,
            AudioFormat.CHANNEL_OUT_MONO,
            AudioFormat.ENCODING_PCM_16BIT,
        )
        val bufBytes = (pcm.size * 2).coerceAtLeast(minBuf.coerceAtLeast(2048))

        val track = AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                    .build()
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(SAMPLE_RATE)
                    .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                    .build()
            )
            .setBufferSizeInBytes(bufBytes)
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()

        try {
            track.write(pcm, 0, pcm.size)
            track.play()
            // write() returns after enqueue, but the kernel buffer still has
            // to drain into the speaker. Suspend for the full preset
            // duration plus a small tail to cover output latency.
            delay((maxEnd * 1000).toLong() + DRAIN_TAIL_MS)
        } finally {
            try { track.stop() } catch (_: Throwable) {}
            track.release()
        }
    }

    private fun sampleFor(type: String, phase: Float): Short {
        val v: Float = when (type) {
            "sine" -> sin(2.0 * PI * phase).toFloat()
            "triangle" -> if (phase < 0.5f) phase * 4f - 1f else 3f - phase * 4f
            "sawtooth" -> phase * 2f - 1f
            else -> if (phase < 0.5f) 1f else -1f // "square" + unknown fallback
        }
        return (AMP * v).toInt().toShort()
    }

    companion object {
        private const val SAMPLE_RATE = 44100
        // ~0.18 of full-scale 16-bit — comfortable mid-volume so the alert
        // is audible against following speech audio without clipping.
        private const val AMP = 6000
        private const val DRAIN_TAIL_MS = 60L
    }
}

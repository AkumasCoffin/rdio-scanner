package solutions.saubeo.rdioscanner.audio

import android.media.AudioManager
import android.media.ToneGenerator

/**
 * Short tone feedback for button taps — a cheap stand-in for the webapp's
 * Web Audio oscillator beeps (`RdioScannerBeepStyle` activate / deactivate /
 * denied). Each category picks a tone from a different ITU/CDMA family so the
 * pitches are audibly distinct, and we stop the previous tone before starting
 * a new one so rapid taps don't swallow each other.
 */
class ClickSound {
    private val tone: ToneGenerator? = try {
        ToneGenerator(AudioManager.STREAM_SYSTEM, 70)
    } catch (t: Throwable) {
        null
    }

    private fun play(toneType: Int, durationMs: Int) {
        val t = tone ?: return
        t.stopTone()
        t.startTone(toneType, durationMs)
    }

    /** Short, mid-pitched ack — used for plain taps (SEARCH, REPLAY, SKIP, SELECT). */
    fun click() = play(ToneGenerator.TONE_PROP_ACK, 35)

    /** Rising "on" tone for activating a toggle (LIVE FEED on, HOLD on, PAUSE on). */
    fun activate() = play(ToneGenerator.TONE_CDMA_CONFIRM, 70)

    /** Falling "off" tone for deactivating a toggle (LIVE FEED off, HOLD off, PAUSE off). */
    fun deactivate() = play(ToneGenerator.TONE_CDMA_ABBR_ALERT, 70)

    /** Harsh error tone when a button is pressed but pre-conditions aren't met. */
    fun denied() = play(ToneGenerator.TONE_SUP_ERROR, 120)

    fun release() {
        tone?.release()
    }
}

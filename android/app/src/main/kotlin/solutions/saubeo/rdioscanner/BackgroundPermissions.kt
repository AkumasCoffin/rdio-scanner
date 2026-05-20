package solutions.saubeo.rdioscanner

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import android.util.Log
import androidx.core.content.getSystemService

/**
 * Helpers for the OS-level "run in background" permission. There is no
 * single runtime permission for "background execution" on Android, but the
 * closest user-facing equivalent is the battery-optimization exemption — a
 * system dialog the user has to grant before OEMs (especially Samsung) stop
 * applying NETD DNS blocks and other deep-background restrictions to apps
 * that hold a valid foreground service.
 *
 * Pattern mirrors POST_NOTIFICATIONS: fire the system prompt once at first
 * launch, respect the answer, and re-check the runtime state on every code
 * path that wants to behave differently when granted vs. denied. The user
 * manages it from then on via Settings > Apps > Rdio Scanner > Battery.
 */
object BackgroundPermissions {
    private const val TAG = "BackgroundPermissions"

    /**
     * `true` when the OS has granted this app the battery-optimization
     * exemption, i.e. the user picked "Allow" in the system dialog or in
     * Settings. Source of truth for every "should we run in background?"
     * decision — checked at runtime (not cached) so revocation in system
     * Settings is honored on the next gate.
     */
    fun canRunInBackground(context: Context): Boolean {
        val pm = context.applicationContext.getSystemService<PowerManager>() ?: return false
        return pm.isIgnoringBatteryOptimizations(context.applicationContext.packageName)
    }

    /**
     * Fires the system `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` dialog. Pops
     * a real OS prompt with Allow / Don't allow, identical UX to the
     * notification-permission dialog. Returns true if the intent was
     * dispatched, false if it couldn't be (e.g. emulator without the
     * settings activity). Caller is responsible for re-checking
     * [canRunInBackground] after the dialog closes — the launcher doesn't
     * deliver the user's choice as a result.
     */
    fun requestBatteryOptimizationExemption(context: Context): Boolean {
        val pkg = context.applicationContext.packageName
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
            data = Uri.parse("package:$pkg")
            // Required when launching from a non-Activity context (e.g.
            // application context). Safe to set even from an Activity.
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        return try {
            context.startActivity(intent)
            true
        } catch (t: Throwable) {
            Log.w(TAG, "requestBatteryOptimizationExemption failed: ${t.message}")
            false
        }
    }
}

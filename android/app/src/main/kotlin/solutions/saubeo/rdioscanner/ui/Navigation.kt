package solutions.saubeo.rdioscanner.ui

import android.util.Log
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.ui.screens.ConnectScreen
import solutions.saubeo.rdioscanner.ui.screens.LivefeedScreen
import solutions.saubeo.rdioscanner.ui.screens.SearchScreen
import solutions.saubeo.rdioscanner.ui.screens.SelectorScreen
import solutions.saubeo.rdioscanner.ui.theme.RdioBackground

private const val TAG = "RdioNav"

private object Routes {
    const val CONNECT = "connect"
    const val LIVEFEED = "livefeed"
    const val SELECTOR = "selector"
    const val SEARCH = "search"
}

@Composable
fun RdioApp() {
    val vm: ScannerViewModel = viewModel()
    val navController = rememberNavController()
    val state by vm.state.collectAsStateWithLifecycle()

    // Cold starts land on the Connect screen so the user can pick a profile.
    // The ON_RESUME hook below only auto-retries once we've actually been
    // Connected in this process — fixes the "stuck on connect page after
    // returning from background" case where the socket died (Doze froze
    // the backoff timer, or transient network loss) and the user would
    // otherwise have to manually re-tap their profile.
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) vm.onActivityResumed()
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    LaunchedEffect(state) {
        val route = navController.currentBackStackEntry?.destination?.route
        Log.d(TAG, "LaunchedEffect(state): state=$state, route=$route")
        when (state) {
            ConnectionState.Connected -> {
                if (route == Routes.CONNECT || route == null) {
                    // Keep CONNECT on the back stack (inclusive = false) so a
                    // system-back press from LIVEFEED returns to the picker
                    // instead of exiting the app to the home screen. Without
                    // this, the first connect popped the only entry and the
                    // next back tap killed the process — which read to
                    // multi-profile users as "tapping connection 2 kicks me
                    // out of the app."
                    Log.d(TAG, "  -> navigate(LIVEFEED)")
                    navController.navigate(Routes.LIVEFEED) {
                        popUpTo(Routes.CONNECT) { inclusive = false }
                        launchSingleTop = true
                    }
                } else {
                    Log.d(TAG, "  -> Connected but already past CONNECT, no nav")
                }
            }
            ConnectionState.Disconnected,
            ConnectionState.AuthFailed,
            ConnectionState.Expired,
            ConnectionState.TooMany,
            is ConnectionState.Error -> {
                if (route != Routes.CONNECT) {
                    Log.d(TAG, "  -> popBackStack(CONNECT, inclusive=false)")
                    // Pop back to the existing CONNECT entry rather than
                    // pushing a fresh one — paired with the inclusive=false
                    // navigate above, this keeps the stack tidy at [CONNECT]
                    // after a disconnect, no matter how many connect/
                    // disconnect cycles happened during the session.
                    navController.popBackStack(Routes.CONNECT, inclusive = false)
                } else {
                    Log.d(TAG, "  -> Disconnected and already on CONNECT, no nav")
                }
            }
            else -> Log.d(TAG, "  -> intermediate state, no nav")
        }
    }

    RdioBackground {
        NavHost(navController = navController, startDestination = Routes.CONNECT) {
            composable(Routes.CONNECT) {
                ConnectScreen(vm = vm)
            }
            composable(Routes.LIVEFEED) {
                LivefeedScreen(
                    vm = vm,
                    onOpenSelector = { navController.navigate(Routes.SELECTOR) },
                    onOpenSearch = { navController.navigate(Routes.SEARCH) },
                )
            }
            composable(Routes.SELECTOR) {
                SelectorScreen(vm = vm, onBack = { navController.popBackStack() })
            }
            composable(Routes.SEARCH) {
                SearchScreen(vm = vm, onBack = { navController.popBackStack() })
            }
        }
    }
}

package solutions.saubeo.rdioscanner.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
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

    // Intentionally NOT calling vm.tryReconnect() here: cold starts should
    // land on the Connect screen so the user picks which profile to use.
    // If the process was still alive (warm return from background) the
    // repository already holds a Connected state, and the LaunchedEffect
    // below will put us straight on the Livefeed.

    LaunchedEffect(state) {
        when (state) {
            ConnectionState.Connected -> {
                val route = navController.currentBackStackEntry?.destination?.route
                if (route == Routes.CONNECT || route == null) {
                    navController.navigate(Routes.LIVEFEED) {
                        popUpTo(Routes.CONNECT) { inclusive = true }
                        launchSingleTop = true
                    }
                }
            }
            ConnectionState.Disconnected,
            ConnectionState.AuthFailed,
            ConnectionState.Expired,
            ConnectionState.TooMany,
            is ConnectionState.Error -> {
                if (navController.currentBackStackEntry?.destination?.route != Routes.CONNECT) {
                    navController.navigate(Routes.CONNECT) {
                        popUpTo(Routes.LIVEFEED) { inclusive = true }
                        launchSingleTop = true
                    }
                }
            }
            else -> Unit
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

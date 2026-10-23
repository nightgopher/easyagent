package com.hwj.easyagent.mobile.updates

import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import java.io.File

internal interface UpdatePreferences {
    var automatic: Boolean
    var lastAttempt: Long
    var dismissedTag: String
}

data class UpdateState(
    val visible: Boolean = false,
    val automatic: Boolean = true,
    val checking: Boolean = false,
    val checked: Boolean = false,
    val update: AppUpdate? = null,
    val progress: Int? = null,
    val file: File? = null,
    val verifying: Boolean = false,
    val error: String? = null,
) {
    val busy: Boolean get() = checking || progress != null || verifying
}

/** App-scoped state survives screen/host switches; update settings are separate from host credentials. */
class AppUpdater internal constructor(
    val installed: InstalledVersion,
    private val client: UpdateClient,
    private val directory: File,
    private val preferences: UpdatePreferences,
    private val verifyApk: (File, AppUpdate) -> Unit,
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate),
    private val clock: () -> Long = System::currentTimeMillis,
) {
    private val mutableState = MutableStateFlow(UpdateState(automatic = preferences.automatic))
    val state = mutableState.asStateFlow()
    private var job: Job? = null
    private var notifyResult = false

    fun foreground() {
        val elapsed = clock() - preferences.lastAttempt
        if (preferences.automatic && (preferences.lastAttempt == 0L || elapsed !in 0 until CHECK_INTERVAL)) check(false)
    }

    fun open() {
        mutableState.update { it.copy(visible = true) }
        if (!state.value.busy) check(true)
    }

    fun setAutomatic(enabled: Boolean) {
        preferences.automatic = enabled
        if (!enabled) notifyResult = false
        mutableState.update { it.copy(automatic = enabled) }
    }

    fun dismiss() {
        notifyResult = false
        state.value.update?.let { preferences.dismissedTag = it.tag }
        mutableState.update { it.copy(visible = false) }
    }

    fun check(manual: Boolean = true) {
        if (job?.isActive == true) return
        notifyResult = !manual
        preferences.lastAttempt = clock()
        mutableState.update { it.copy(checking = true, error = null, visible = it.visible || manual) }
        job = scope.launch {
            try {
                val update = client.latest(installed)
                val cached = update?.let { File(directory, "${it.tag}.apk") }?.takeIf { it.isFile }
                val valid = if (cached != null) withContext(Dispatchers.IO) {
                    try { verifyDownloadedFile(cached, update); verifyApk(cached, update); cached }
                    catch (_: Exception) { cached.delete(); null }
                } else null
                mutableState.update { it.copy(checked = true, update = update, file = valid,
                    visible = it.visible || (notifyResult && update != null && update.tag != preferences.dismissedTag)) }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { fail(e) }
            finally { mutableState.update { it.copy(checking = false) } }
        }
    }

    fun download() {
        val update = state.value.update ?: return
        if (job?.isActive == true) return
        mutableState.update { it.copy(progress = 0, file = null, error = null) }
        job = scope.launch {
            try {
                val downloading = currentCoroutineContext().job
                val file = client.download(update, directory, { percent ->
                    mutableState.update { if (downloading.isActive) it.copy(progress = percent) else it }
                }, verifyApk)
                mutableState.update { it.copy(file = file) }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { fail(e) }
            finally { mutableState.update { it.copy(progress = null) } }
        }
    }

    fun cancelDownload() {
        if (state.value.progress != null) job?.cancel()
    }

    /** Revalidate immediately before handing the file to the system, including after permission settings. */
    fun install(launch: (File) -> Unit) {
        val update = state.value.update ?: return
        val file = state.value.file ?: return
        if (job?.isActive == true) return
        mutableState.update { it.copy(verifying = true, error = null) }
        job = scope.launch {
            try {
                withContext(Dispatchers.IO) { verifyDownloadedFile(file, update); verifyApk(file, update) }
                launch(file)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                // Preserve a valid download when the OS cannot open its installer.
                if (e is UpdateException) { file.delete(); mutableState.update { it.copy(file = null) } }
                fail(e)
            } finally { mutableState.update { it.copy(verifying = false) } }
        }
    }

    fun installationError(message: String) { mutableState.update { it.copy(error = message) } }
    private fun fail(error: Exception) { mutableState.update { it.copy(error = (error as? UpdateException)?.message ?: "更新操作失败，请重试") } }
    internal fun close() { scope.cancel() }

    companion object { internal const val CHECK_INTERVAL = 6 * 60 * 60 * 1000L }
}

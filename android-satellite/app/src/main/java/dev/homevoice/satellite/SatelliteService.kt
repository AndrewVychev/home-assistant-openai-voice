package dev.homevoice.satellite

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.graphics.drawable.Icon
import android.os.Build
import android.os.IBinder
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONObject
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

class SatelliteService : Service() {
    private val client = OkHttpClient.Builder().pingInterval(20, TimeUnit.SECONDS).build()
    private val scheduler = Executors.newSingleThreadScheduledExecutor()
    private val stopping = AtomicBoolean(false)
    private var socket: WebSocket? = null
    private var audio: AudioEngine? = null
    private var toolExecuted = false
    private val assistantText = StringBuilder()

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> finish("Остановлено")
            ACTION_START -> {
                showForeground("Подключаюсь…")
                connect(
                    gateway = intent.getStringExtra(EXTRA_GATEWAY).orEmpty(),
                    token = intent.getStringExtra(EXTRA_TOKEN).orEmpty(),
                    provider = intent.getStringExtra(EXTRA_PROVIDER).orEmpty(),
                    satelliteId = intent.getStringExtra(EXTRA_SATELLITE_ID).orEmpty(),
                )
            }
        }
        return START_NOT_STICKY
    }

    private fun connect(gateway: String, token: String, provider: String, satelliteId: String) {
        stopping.set(false)
        toolExecuted = false
        assistantText.clear()
        audio = AudioEngine(this) { frame -> socket?.send(frame.toByteString()) == true }
        val separator = if (gateway.contains('?')) "&" else "?"
        val url = gateway + separator + listOf(
            "protocol=1",
            "transport=binary",
            "response_mode=audio",
            "provider=${encode(provider)}",
            "satellite_id=${encode(satelliteId)}",
        ).joinToString("&")
        val request = Request.Builder().url(url).apply {
            if (token.isNotBlank()) header("Authorization", "Bearer $token")
        }.build()
        socket = client.newWebSocket(request, Listener())
    }

    private inner class Listener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            publish("Gateway подключён, жду Live API…", true)
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = runCatching { JSONObject(text) }.getOrElse {
                fail("Некорректный ответ gateway")
                return
            }
            when (message.optString("type")) {
                "ready" -> {
                    publish("Слушаю · ${message.optString("provider")} · ${message.optString("model")}", true)
                    audio?.startCapture()
                }
                "input_transcript" -> publish("Ты: ${message.optString("text")}", true)
                "output_transcript", "text_delta" -> {
                    val part = message.optString("text")
                    assistantText.append(part)
                    audio?.stopCapture()
                    publish("Ассистент: ${assistantText.toString().trim()}", true)
                }
                "tool_call" -> {
                    toolExecuted = true
                    audio?.stopCapture()
                    publish("Выполняю команду дома…", true)
                }
                "ha_result" -> publish("Home Assistant ответил", true)
                "turn_complete" -> completeTurn()
                "error" -> fail(message.optString("message", "Ошибка gateway"))
            }
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
            audio?.stopCapture()
            audio?.play(bytes.toByteArray())
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            val suffix = response?.let { " · HTTP ${it.code}" }.orEmpty()
            fail("Соединение: ${t.message ?: t.javaClass.simpleName}$suffix")
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            if (!stopping.get()) fail("Gateway закрыл соединение: $code $reason")
        }
    }

    private fun completeTurn() {
        val clarification = !toolExecuted && requestsClarification(assistantText.toString())
        val delay = audio?.remainingPlaybackMillis() ?: 150L
        scheduler.schedule({
            if (clarification && !stopping.get()) {
                assistantText.clear()
                audio?.resetPlayback()
                audio?.startCapture()
                publish("Слушаю уточнение…", true)
            } else {
                finish("Сессия завершена")
            }
        }, delay, TimeUnit.MILLISECONDS)
    }

    private fun requestsClarification(text: String): Boolean {
        val normalized = text.trim().lowercase()
        return normalized.contains('?') || listOf("уточни", "какой ", "какая ", "какое ", "какие ", "что именно").any(normalized::contains)
    }

    private fun fail(message: String) = finish("Ошибка: $message")

    private fun finish(message: String) {
        if (!stopping.compareAndSet(false, true)) return
        audio?.close()
        audio = null
        socket?.close(1000, "session ended")
        socket = null
        publish(message, false)
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun showForeground(status: String) {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel(CHANNEL_ID, "Home Voice", NotificationManager.IMPORTANCE_LOW))
        val notification = notification(status)
        if (Build.VERSION.SDK_INT >= 30) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
        publish(status, true)
    }

    private fun notification(status: String): Notification {
        val activity = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val stop = PendingIntent.getService(
            this,
            1,
            Intent(this, SatelliteService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_btn_speak_now)
            .setContentTitle("Home Voice Satellite")
            .setContentText(status)
            .setContentIntent(activity)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(Icon.createWithResource(this, android.R.drawable.ic_media_pause), "Остановить", stop).build())
            .build()
    }

    private fun publish(status: String, running: Boolean) {
        getSystemService(NotificationManager::class.java)?.notify(NOTIFICATION_ID, notification(status))
        sendBroadcast(
            Intent(ACTION_STATUS)
                .setPackage(packageName)
                .putExtra(EXTRA_STATUS, status)
                .putExtra(EXTRA_RUNNING, running),
        )
    }

    override fun onDestroy() {
        audio?.close()
        socket?.cancel()
        scheduler.shutdownNow()
        client.dispatcher.executorService.shutdown()
        super.onDestroy()
    }

    private fun encode(value: String): String = URLEncoder.encode(value, StandardCharsets.UTF_8.toString())

    companion object {
        const val ACTION_START = "dev.homevoice.satellite.START"
        const val ACTION_STOP = "dev.homevoice.satellite.STOP"
        const val ACTION_STATUS = "dev.homevoice.satellite.STATUS"
        const val EXTRA_GATEWAY = "gateway"
        const val EXTRA_TOKEN = "token"
        const val EXTRA_PROVIDER = "provider"
        const val EXTRA_SATELLITE_ID = "satellite_id"
        const val EXTRA_STATUS = "status"
        const val EXTRA_RUNNING = "running"
        private const val CHANNEL_ID = "homevoice-satellite"
        private const val NOTIFICATION_ID = 7
    }
}

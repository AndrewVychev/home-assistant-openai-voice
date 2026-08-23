package dev.homevoice.satellite

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.Spinner
import android.widget.TextView
import android.widget.Toast
import androidx.core.content.ContextCompat

@SuppressLint("SetTextI18n")
class MainActivity : Activity() {
    private lateinit var gateway: EditText
    private lateinit var token: EditText
    private lateinit var satelliteId: EditText
    private lateinit var provider: Spinner
    private lateinit var status: TextView
    private lateinit var talk: Button
    private var running = false

    private val statusReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            running = intent?.getBooleanExtra(SatelliteService.EXTRA_RUNNING, false) == true
            status.text = intent?.getStringExtra(SatelliteService.EXTRA_STATUS) ?: "Остановлено"
            talk.text = if (running) "Остановить" else "Говорить"
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        title = "Home Voice Satellite"
        setContentView(buildContent())
        loadSettings()
        requestPermissionsIfNeeded()
    }

    override fun onStart() {
        super.onStart()
        val filter = IntentFilter(SatelliteService.ACTION_STATUS)
        ContextCompat.registerReceiver(this, statusReceiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
    }

    override fun onStop() {
        unregisterReceiver(statusReceiver)
        super.onStop()
    }

    private fun buildContent(): LinearLayout {
        val padding = (20 * resources.displayMetrics.density).toInt()
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, padding, padding, padding)
            addView(TextView(context).apply {
                text = "Home Voice Satellite"
                textSize = 26f
                setTypeface(typeface, Typeface.BOLD)
            })
            addView(label("Gateway WebSocket"))
            gateway = field("ws://IP-МАКА:3000/live")
            addView(gateway)
            addView(label("Satellite token"))
            token = field("Оставь пустым, если auth выключен")
            token.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            addView(token)
            addView(label("Название комнаты/сателлита"))
            satelliteId = field("android-room")
            addView(satelliteId)
            addView(label("Live provider"))
            provider = Spinner(context).apply {
                adapter = ArrayAdapter(context, android.R.layout.simple_spinner_dropdown_item, listOf("openai", "gemini"))
            }
            addView(provider)
            status = TextView(context).apply {
                text = "Готов к подключению"
                textSize = 18f
                gravity = Gravity.CENTER
                setPadding(0, padding, 0, padding)
            }
            addView(status)
            talk = Button(context).apply {
                text = "Говорить"
                textSize = 20f
                setOnClickListener { toggleSatellite() }
            }
            addView(talk, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
            addView(TextView(context).apply {
                text = "Нажми «Говорить», дождись статуса «Слушаю» и скажи одну команду. После ответа сессия закроется автоматически."
                setPadding(0, padding, 0, 0)
            })
        }
    }

    private fun label(text: String) = TextView(this).apply {
        this.text = text
        setPadding(0, 18, 0, 4)
    }

    private fun field(hint: String) = EditText(this).apply {
        this.hint = hint
        isSingleLine = true
    }

    private fun loadSettings() {
        val settings = SatelliteSettings.load(this)
        gateway.setText(settings.gateway)
        token.setText(settings.token)
        satelliteId.setText(settings.satelliteId)
        provider.setSelection(if (settings.provider == "gemini") 1 else 0)
    }

    private fun toggleSatellite() {
        if (running) {
            startService(Intent(this, SatelliteService::class.java).setAction(SatelliteService.ACTION_STOP))
            return
        }
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            requestPermissionsIfNeeded()
            Toast.makeText(this, "Разреши доступ к микрофону", Toast.LENGTH_SHORT).show()
            return
        }
        val settings = SatelliteSettings(
            gateway = gateway.text.toString().trim(),
            token = token.text.toString().trim(),
            provider = provider.selectedItem.toString(),
            satelliteId = satelliteId.text.toString().trim().ifEmpty { "android-room" },
        )
        if (!settings.gateway.startsWith("ws://") && !settings.gateway.startsWith("wss://")) {
            Toast.makeText(this, "Gateway должен начинаться с ws:// или wss://", Toast.LENGTH_LONG).show()
            return
        }
        settings.save(this)
        val intent = Intent(this, SatelliteService::class.java)
            .setAction(SatelliteService.ACTION_START)
            .putExtra(SatelliteService.EXTRA_GATEWAY, settings.gateway)
            .putExtra(SatelliteService.EXTRA_TOKEN, settings.token)
            .putExtra(SatelliteService.EXTRA_PROVIDER, settings.provider)
            .putExtra(SatelliteService.EXTRA_SATELLITE_ID, settings.satelliteId)
        startForegroundService(intent)
        running = true
        talk.text = "Остановить"
        status.text = "Подключаюсь…"
    }

    private fun requestPermissionsIfNeeded() {
        val permissions = mutableListOf<String>()
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            permissions += Manifest.permission.RECORD_AUDIO
        }
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            permissions += Manifest.permission.POST_NOTIFICATIONS
        }
        if (permissions.isNotEmpty()) requestPermissions(permissions.toTypedArray(), 10)
    }
}

package dev.homevoice.satellite

import android.content.Context

data class SatelliteSettings(
    val gateway: String,
    val token: String,
    val provider: String,
    val satelliteId: String,
    val activationMode: String,
) {
    fun save(context: Context) {
        context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE).edit()
            .putString("gateway", gateway)
            .putString("token", token)
            .putString("provider", provider)
            .putString("satellite_id", satelliteId)
            .putString("activation_mode", activationMode)
            .apply()
    }

    companion object {
        private const val PREFERENCES = "satellite"

        fun load(context: Context): SatelliteSettings {
            val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
            return SatelliteSettings(
                gateway = preferences.getString("gateway", "ws://192.168.1.2:3000/live").orEmpty(),
                token = preferences.getString("token", "").orEmpty(),
                provider = preferences.getString("provider", "openai").orEmpty(),
                satelliteId = preferences.getString("satellite_id", "android-room").orEmpty(),
                activationMode = preferences.getString("activation_mode", "wake").orEmpty(),
            )
        }
    }
}

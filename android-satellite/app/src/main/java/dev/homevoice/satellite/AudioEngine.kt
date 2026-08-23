package dev.homevoice.satellite

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTrack
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread

class AudioEngine(private val context: Context, private val onInput: (ByteArray) -> Boolean) {
    private val capturing = AtomicBoolean(false)
    private var recorder: AudioRecord? = null
    private var playback: AudioTrack? = null
    private var playbackBytes = 0L
    private var playbackStartedAt = 0L

    fun startCapture() {
        if (!capturing.compareAndSet(false, true)) return
        if (context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            capturing.set(false)
            throw SecurityException("RECORD_AUDIO permission is required")
        }
        val format = AudioFormat.Builder()
            .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
            .setSampleRate(INPUT_RATE)
            .setChannelMask(AudioFormat.CHANNEL_IN_MONO)
            .build()
        val minimum = AudioRecord.getMinBufferSize(INPUT_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        val created = AudioRecord.Builder()
            .setAudioSource(MediaRecorder.AudioSource.VOICE_RECOGNITION)
            .setAudioFormat(format)
            .setBufferSizeInBytes(maxOf(minimum, FRAME_BYTES * 8))
            .build()
        recorder = created
        if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(created.audioSessionId)?.enabled = true
        if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(created.audioSessionId)?.enabled = true
        created.startRecording()
        thread(name = "homevoice-capture", isDaemon = true) {
            val frame = ByteArray(FRAME_BYTES)
            while (capturing.get()) {
                val read = created.read(frame, 0, frame.size, AudioRecord.READ_BLOCKING)
                if (read > 0 && !onInput(frame.copyOf(read))) break
            }
        }
    }

    fun stopCapture() {
        if (!capturing.compareAndSet(true, false)) return
        runCatching { recorder?.stop() }
        recorder?.release()
        recorder = null
    }

    @Synchronized
    fun play(chunk: ByteArray) {
        if (playback == null) {
            val minimum = AudioTrack.getMinBufferSize(OUTPUT_RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT)
            playback = AudioTrack.Builder()
                .setAudioAttributes(
                    AudioAttributes.Builder()
                        .setUsage(AudioAttributes.USAGE_ASSISTANCE_ACCESSIBILITY)
                        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                        .build(),
                )
                .setAudioFormat(
                    AudioFormat.Builder()
                        .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                        .setSampleRate(OUTPUT_RATE)
                        .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                        .build(),
                )
                .setTransferMode(AudioTrack.MODE_STREAM)
                .setBufferSizeInBytes(maxOf(minimum, OUTPUT_RATE))
                .build()
            playbackStartedAt = System.currentTimeMillis()
            playback?.play()
        }
        playback?.write(chunk, 0, chunk.size, AudioTrack.WRITE_BLOCKING)
        playbackBytes += chunk.size
    }

    @Synchronized
    fun remainingPlaybackMillis(): Long {
        val total = playbackBytes * 1000L / (OUTPUT_RATE * PCM_BYTES)
        val elapsed = System.currentTimeMillis() - playbackStartedAt
        return (total - elapsed).coerceAtLeast(0) + 150
    }

    @Synchronized
    fun resetPlayback() {
        runCatching { playback?.stop() }
        playback?.release()
        playback = null
        playbackBytes = 0
        playbackStartedAt = 0
    }

    fun close() {
        stopCapture()
        resetPlayback()
    }

    companion object {
        const val INPUT_RATE = 16_000
        const val OUTPUT_RATE = 24_000
        private const val PCM_BYTES = 2
        private const val FRAME_BYTES = INPUT_RATE * PCM_BYTES * 20 / 1000
    }
}

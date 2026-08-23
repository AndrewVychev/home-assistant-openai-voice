# Архитектура комнатных голосовых сателлитов

```text
Android сейчас                    ReSpeaker ESP32-S3 потом
┌──────────────────┐              ┌──────────────────────┐
│ AudioRecord 16k  │              │ I²S mic 16k          │
│ AudioTrack 24k   │              │ I²S speaker 24k      │
│ reconnect/status │              │ reconnect/status     │
└────────┬─────────┘              └──────────┬───────────┘
         └──────── Satellite Protocol v1 ────┘
                              │ binary PCM + JSON
                              ▼
                     ┌──────────────────┐
                     │ Go gateway / N100│
                     │ server wake word │
                     │ pre-roll + auth  │
                     │ context + HA     │
                     └───────┬──────────┘
                             ├── OpenAI Realtime
                             ├── Gemini Live
                             └── Home Assistant
```

## Границы компонентов

Satellite отвечает только за:

- захват PCM16 mono 16 kHz;
- отправку бинарных audio frames;
- воспроизведение PCM16 mono 24 kHz;
- индикацию состояний: idle, connecting, listening, thinking, speaking, error.

Gateway отвечает за всё изменяемое и секретное:

- ключи OpenAI/Gemini и токен Home Assistant;
- локальный wake word «Куза» и секундный pre-roll для каждого потока;
- список сущностей и комнат;
- tool calling, guard rules и подтверждение фактического состояния;
- краткосрочный контекст по `satellite_id`;
- выбор provider и учёт стоимости.

Благодаря этой границе ESP32 не знает ни API моделей, ни схему Home Assistant. Замена OpenAI на Gemini или изменение guard rules не требует прошивать комнаты заново.

## Этапы

### 1. Android-прототип

Приложение `android-satellite` имеет основной режим `Серверная «Куза»` и диагностический push-to-talk. В основном режиме foreground service постоянно передаёт PCM по LAN, но платная Live-сессия существует только между `wake_detected` и `turn_complete`.

### 2. Серверная активация

Gateway запускает отдельное состояние wake detector для каждого `satellite_id`, хранит секундный PCM pre-roll и после «Кузы» временно переключает тот же поток в OpenAI/Gemini. Клиент не содержит TensorFlow Lite и остаётся аппаратно независимым.

### 3. ReSpeaker ESP32-S3

Прошивка реализует тот же Protocol v1. Android `AudioRecord/AudioTrack` заменяется на I²S mic/speaker; wake word остаётся на gateway. Для первой версии сохраняется half-duplex: во время ответа микрофон заглушён. Это заметно упрощает echo cancellation.

### 4. Постоянная домашняя система

- индивидуальный отзываемый токен на каждый satellite;
- `wss://` через локальный reverse proxy;
- mDNS discovery `_homevoice._tcp` вместо ручного IP;
- heartbeat, reconnect с backoff и локальный сигнал ошибки;
- OTA-прошивки ESP32 и метрики уровня микрофона/качества Wi-Fi;
- затем, при необходимости, full-duplex с AEC.

## Важные решения

- Бинарный PCM вместо base64 экономит примерно треть сетевого трафика и RAM на ESP32.
- Сессия с облаком создаётся только после серверного wake word.
- Контекст изолирован стабильным `satellite_id`, например `kitchen` или `bedroom`.
- Никаких прямых запросов Android/ESP32 к Home Assistant.
- Текущий cleartext `ws://` разрешён только для прототипа в доверенной Wi-Fi сети; финальная установка использует `wss://` и отдельные device tokens.

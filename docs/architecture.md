# Архитектура комнатных голосовых сателлитов

```text
Android сейчас                    ReSpeaker ESP32-S3 потом
┌──────────────────┐              ┌──────────────────────┐
│ manual activation│              │ local wake word      │
│ AudioRecord 16k  │              │ I²S mic + DSP 16k    │
│ AudioTrack 24k   │              │ I²S speaker 24k      │
└────────┬─────────┘              └──────────┬───────────┘
         └──────── Satellite Protocol v1 ────┘
                              │ binary PCM + JSON
                              ▼
                     ┌──────────────────┐
                     │ Go gateway / N100│
                     │ auth + policy    │
                     │ context + HA     │
                     └───────┬──────────┘
                             ├── OpenAI Realtime
                             ├── Gemini Live
                             └── Home Assistant
```

## Границы компонентов

Satellite отвечает только за:

- локальную активацию (`Manual`, `Kuza`, `WakeNet` — реализации одного контракта);
- захват PCM16 mono 16 kHz и короткий pre-roll;
- отправку бинарных audio frames;
- воспроизведение PCM16 mono 24 kHz;
- индикацию состояний: idle, connecting, listening, thinking, speaking, error.

Gateway отвечает за всё изменяемое и секретное:

- ключи OpenAI/Gemini и токен Home Assistant;
- список сущностей и комнат;
- tool calling, guard rules и подтверждение фактического состояния;
- краткосрочный контекст по `satellite_id`;
- выбор provider и учёт стоимости.

Благодаря этой границе ESP32 не знает ни API моделей, ни схему Home Assistant. Замена OpenAI на Gemini или изменение guard rules не требует прошивать комнаты заново.

## Этапы

### 1. Android-прототип

Приложение `android-satellite` работает по кнопке и запускает foreground service только на одну сессию. Это позволяет проверить микрофон телефона, Wi-Fi, задержку, распознавание и качество динамика без постоянной платы за Live API.

### 2. Локальная активация на Android

Добавляется `ActivationEngine` с кольцевым PCM-буфером. Первая реализация — модель «Куза» через TensorFlow Lite после отдельной проверки лицензии и качества на ARM. После wake отправляются последние 0.5–1.0 секунды pre-roll и текущая команда. Live API до wake не подключается.

### 3. ReSpeaker ESP32-S3

Прошивка повторяет тот же state machine и Protocol v1. Аппаратный слой заменяется на I²S mic/speaker; activation engine — Espressif WakeNet либо совместимая локальная TFLite/ESP-DL модель. Для первой версии остаётся half-duplex: во время ответа микрофон заглушён. Это заметно упрощает echo cancellation.

### 4. Постоянная домашняя система

- индивидуальный отзываемый токен на каждый satellite;
- `wss://` через локальный reverse proxy;
- mDNS discovery `_homevoice._tcp` вместо ручного IP;
- heartbeat, reconnect с backoff и локальный сигнал ошибки;
- OTA-прошивки ESP32 и метрики уровня микрофона/качества Wi-Fi;
- затем, при необходимости, full-duplex с AEC.

## Важные решения

- Бинарный PCM вместо base64 экономит примерно треть сетевого трафика и RAM на ESP32.
- Сессия с облаком создаётся только после активации.
- Контекст изолирован стабильным `satellite_id`, например `kitchen` или `bedroom`.
- Никаких прямых запросов Android/ESP32 к Home Assistant.
- Текущий cleartext `ws://` разрешён только для прототипа в доверенной Wi-Fi сети; финальная установка использует `wss://` и отдельные device tokens.

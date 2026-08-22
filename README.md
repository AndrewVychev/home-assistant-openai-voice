# Home Voice: Go × Gemini/OpenAI Live × Home Assistant

Центральный Go-шлюз для голосового управления Home Assistant. Он держит секреты, получает свежие сущности и комнаты HA перед каждой Live-сессией и выполняет только разрешённые function calls. Gemini Live и OpenAI Realtime подключены как взаимозаменяемые реализации одного интерфейса. Веб-страница остаётся диагностическим клиентом для разработки на Mac; комнатные микрофоны подключаются к тому же WebSocket-протоколу отдельными сателлитами.

## Локальный запуск

Нужны Go 1.24+ и Node.js 20+ (Node нужен только для локальных TensorFlow.js-ассетов диагностической страницы).

```bash
cp .env.example .env
npm install
go run ./cmd/gateway
```

Открой `http://127.0.0.1:3000`.

### Тест без браузера

Gateway должен быть запущен в одном терминале. Во втором терминале можно отправить текстовую команду:

```bash
go run ./cmd/satellite -mode text -provider gemini "какое состояние Kitchen Main?"
go run ./cmd/satellite -mode text -provider openai "какое состояние Kitchen Main?"
```

Или использовать микрофон и динамик Mac напрямую:

```bash
go run ./cmd/satellite -mode voice -provider openai
```

Для постоянного локального wake word сначала установи русскую модель «Куза»:

```bash
make setup-kuza
make wake
```

Теперь сателлит постоянно ждёт «Ку́за». До срабатывания облачная модель не вызывается. После ответа сателлит возвращается к локальному ожиданию. Встроенный порог модели — `0.85`; его можно переопределить флагом `-wake-threshold`. Снижение порога повышает чувствительность и число ложных срабатываний.

Для проверки без подключения к облачному API и без расходов произнеси фразу несколько раз: `./bin/homevoice-satellite -mode wake-test`. Режим показывает максимальный score каждую секунду и считает успешные срабатывания. VAD при необходимости можно вернуть параметром `-vad-threshold 0.25`.

Старый официальный `Hey Jarvis` остаётся запасным вариантом:

```bash
make setup-wakeword
./bin/homevoice-satellite -mode wake-test -wake-engine openwakeword
./bin/homevoice-satellite -mode wake -wake-engine openwakeword
```

Либо один раз собрать стабильные бинарники:

```bash
make build
./bin/homevoice-satellite -mode voice
```

При первом запуске macOS запросит доступ терминала к микрофону. Голосовой клиент завершает сессию после выполненной команды и ответа, чтобы не расходовать Live API в простое. Если ассистент попросил уточнение, микрофон включится снова. Для первого теста лучше использовать наушники, чтобы звук динамика не попадал обратно в микрофон.

```dotenv
GEMINI_API_KEY=...
OPENAI_API_KEY=...
HA_TOKEN=...
HA_URL=http://homeassistant.local
VOICE_PROVIDER=gemini
GEMINI_LIVE_MODEL=gemini-3.1-flash-live-preview
OPENAI_REALTIME_MODEL=gpt-realtime-2.1-mini
OPENAI_VOICE=marin
```

Ключи читаются только сервером и не уходят в браузер или комнатный сателлит.

## Схема

```text
микрофон / wake word
        │ PCM 16 kHz
        ▼
комнатный сателлит ──WebSocket──► Go gateway ──► Live Provider interface
                                      │             ├── Gemini Live
                                      │             └── OpenAI Realtime
                                      └──────────► Home Assistant REST/WebSocket
```

Go gateway:

- перед каждой сессией подгружает актуальные `light`, `switch` и `climate`, включая назначенные комнаты;
- не передаёт модели замки, двери, ворота и сигнализацию;
- разрешает только `turn_on`, `turn_off`, `toggle`, `set_temperature` и чтение состояния;
- проверяет фактическое состояние после команды, поэтому ложный HA `500` не превращается в ложную ошибку, если устройство реально переключилось;
- ограничивает температуру диапазоном 10–30 °C;
- держит ответы модели короче пяти слов.

Контракт провайдера находится в `internal/live/live.go`. Для нового API достаточно реализовать `Provider` и `Session`, а затем зарегистрировать адаптер в `internal/gateway/server.go`; логика Home Assistant, защита команд, браузер и сателлит при этом не меняются.

## Wake word

По умолчанию Go-сателлит использует русскую community-модель `Kuza.tflite`. Она локально обрабатывает PCM 16 кГц через openWakeWord-препроцессинг и TensorFlow Lite, а после фразы «Ку́за» запускает выбранный Gemini/OpenAI Live-провайдер. На тесте со встроенным микрофоном Mac модель дала 14 последовательных срабатываний со score `0.85–0.94`. Модель и конфиг скачиваются из [splastunov/microwakeword-ru-model-train](https://github.com/splastunov/microwakeword-ru-model-train) и не коммитятся в репозиторий; у исходного репозитория модели не указана лицензия, поэтому перед распространением готового образа это нужно отдельно согласовать.

Официальный `Hey Jarvis` из openWakeWord доступен через `-wake-engine openwakeword`. В нём mel-spectrogram, audio embeddings, Silero VAD и wake-модель выполняются локально через ONNX Runtime. Облачный Live API ни для одного wake engine до срабатывания не используется. Браузерный TensorFlow.js-вариант «Шо ты голова» оставлен только как старый диагностический эксперимент.

## Проверка

```bash
go test ./...
go vet ./...
```

Проверка статуса:

```bash
curl http://127.0.0.1:3000/api/health
```

## Docker на домашнем сервере

На Linux-машине (например, Intel N100) положи `.env` рядом с `compose.yaml` и запусти:

```bash
docker compose up -d --build
```

`network_mode: host` позволяет шлюзу обращаться к локальному Home Assistant и публикует интерфейс на порту `3000`. Для постоянной системы следующим этапом добавляются комнатные сателлиты с микрофоном, динамиком, локальным wake word и автообновлением.

## Провайдер, режим ответа и usage

Провайдер выбирается в веб-интерфейсе, флагом `-provider gemini|openai` или переменной `VOICE_PROVIDER`. OpenAI использует нативный текстовый output mode. Gemini 3.1 Flash Live генерирует аудио и в режиме «Только текст» шлюз лишь не пересылает аудиочанки браузеру, поэтому это не гарантирует существенного снижения тарификации. Интерфейс показывает фактические usage-токены; точная стоимость сверяется в billing выбранного провайдера.

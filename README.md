# Home Voice: Go × Gemini/OpenAI Live × Home Assistant

Headless Go gateway для голосового управления Home Assistant. Он хранит секреты, получает свежие сущности и комнаты HA перед каждой Live-сессией и выполняет только разрешённые function calls. Gemini Live и OpenAI Realtime подключены как взаимозаменяемые реализации одного интерфейса. Голос приходит от Android, Go CLI или будущих ESP32-S3/ReSpeaker-сателлитов по общему WebSocket-протоколу.

## Gateway на Mac

Нужен Go 1.24+.

```bash
cp .env.example .env
go run ./cmd/gateway
```

Для доступа телефона по локальной сети задай в `.env`:

```dotenv
HOST=0.0.0.0
SATELLITE_TOKEN=
```

Пустой `SATELLITE_TOKEN` допустим только для быстрого теста в доверенной Wi-Fi сети. Для постоянной установки сгенерируй токен командой `openssl rand -hex 32`, запиши его в `.env` и в настройки каждого сателлита.

IP Мака обычно можно узнать командой:

```bash
ipconfig getifaddr en0
```

### Тест через Go CLI

Gateway должен быть запущен в одном терминале. Во втором терминале:

```bash
go run ./cmd/satellite -mode text -provider openai "какое состояние Kitchen Main?"
go run ./cmd/satellite -mode voice -provider openai
```

Если включён `SATELLITE_TOKEN`, CLI читает его из одноимённой переменной или флага `-token`.

Для локального wake word «Куза»:

```bash
make setup-kuza
make wake
```

До срабатывания облачная модель не вызывается. После ответа сателлит возвращается к локальному ожиданию. Встроенный порог модели — `0.85`; его можно переопределить флагом `-wake-threshold`.

## Android-сателлит

Первая версия работает как push-to-talk и открывает Live API только на одну команду. Это позволяет проверить телефон как комнатный микрофон без оплаты облачной сессии в простое.

1. Открой каталог `android-satellite` в Android Studio или собери `./gradlew assembleDebug`.
2. Установи `app-debug.apk` на телефон и разреши микрофон/уведомления.
3. Укажи `ws://IP-МАКА:3000/live`, provider и стабильный `satellite_id`, например `kitchen-phone`.
4. Если на gateway задан `SATELLITE_TOKEN`, вставь тот же токен.
5. Нажми «Говорить», дождись «Слушаю» и произнеси команду.

Android использует foreground service, `AudioRecord` PCM16/16 kHz, бинарный WebSocket transport и `AudioTrack` PCM16/24 kHz. Cleartext `ws://` включён только для LAN-прототипа; финальная система должна использовать `wss://`.

## Схема

```text
Android / Go / ESP32-S3 satellite
      │ local wake or button
      │ PCM16 over WebSocket
      ▼
Go gateway ──► Gemini Live / OpenAI Realtime
      │
      └──────► Home Assistant REST/WebSocket
```

Go gateway:

- перед каждой сессией подгружает актуальные сущности и комнаты;
- не передаёт модели замки, двери, ворота и сигнализацию;
- разрешает только `turn_on`, `turn_off`, `toggle`, `set_temperature` и чтение состояния;
- проверяет фактическое состояние после команды;
- держит облачные ключи и токен Home Assistant только на сервере;
- изолирует краткосрочный контекст по `satellite_id`.

Контракт Live-провайдера находится в `internal/live/live.go`. Satellite Protocol v1 описан в `docs/satellite-protocol-v1.md`, а архитектура перехода Android → ReSpeaker ESP32-S3 — в `docs/architecture.md`.

## Wake word

Go-сателлит использует community-модель `Kuza.tflite`. Она локально обрабатывает PCM 16 kHz, после фразы «Ку́за» запускает выбранный Live provider и передаёт pre-roll, чтобы начало команды не потерялось. Модель скачивается из [splastunov/microwakeword-ru-model-train](https://github.com/splastunov/microwakeword-ru-model-train) и не коммитится; у исходного репозитория не указана лицензия, поэтому распространение готового образа требует отдельной проверки.

Следующий Android-этап — подключить эту модель через сменный `ActivationEngine`. ESP32-S3 реализует тот же контракт через WakeNet либо совместимую локальную модель. Облачный API до wake не подключается.

## Проверка

```bash
go test ./...
go vet ./...
curl http://127.0.0.1:3000/api/health
```

## Docker на домашнем сервере

На Linux-машине положи `.env` рядом с `compose.yaml` и запусти:

```bash
docker compose up -d --build
```

`network_mode: host` позволяет gateway обращаться к локальному Home Assistant и принимать комнатные сателлиты на порту `3000`.

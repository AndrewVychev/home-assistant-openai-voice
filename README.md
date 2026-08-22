# Голосовой прототип Gemini Live × Home Assistant

Экспериментальная ветка `experiment/gemini-live` переносит прототип с OpenAI Realtime на Google Gemini Live API. Браузер захватывает микрофон, локальный Node.js backend проксирует PCM-аудио в Gemini и выполняет разрешённые function calls через Home Assistant API. Ключ Gemini никогда не отправляется в браузер.

## Настройка

```bash
cp .env.example .env
npm install
npm start
```

В `.env` нужны:

```dotenv
GEMINI_API_KEY=...
HA_TOKEN=...
HA_URL=http://homeassistant.local
GEMINI_LIVE_MODEL=gemini-3.1-flash-live-preview
```

Открой `http://127.0.0.1:3000`. Голос пользователя передаётся как mono PCM 16 kHz, аудиоответ Gemini воспроизводится как поток PCM с частотой, указанной API. `gemini-3.1-flash-live-preview` принимает только `AUDIO` как response modality; текст ответа показывается по транскрипту.

## Управление домом

Перед каждой Live-сессией backend получает свежие сущности и комнаты Home Assistant. Модели доступны только два инструмента:

- `control_home_entity` — `turn_on`, `turn_off`, `toggle`, `set_temperature`;
- `get_home_state` — только чтение текущего состояния.

Домены и опасные действия ограничены в `lib/guard.js`. Замки, двери, ворота и сигнализация не передаются модели. Shelly source-switches исключены, используются созданные в Home Assistant сущности `light.*`.

Если команда неоднозначна, сессия остаётся открытой ещё 15 секунд для уточнения. После успешного вызова Home Assistant и итогового ответа сессия закрывается автоматически.

Голосовые ответы ограничены пятью словами, `thinkingLevel` установлен в `minimal`, а `maxOutputTokens` — в 64. Это уменьшает болтовню, задержку и стоимость аудиовыхода.

## Wake word

Фраза `Шо ты голова` распознаётся локальной TensorFlow.js-моделью и не отправляется во внешний API. При первом запуске интерфейс предложит записать 8 вариантов фразы и 8 примеров фонового шума; обученная модель хранится в IndexedDB браузера.

## Usage

Интерфейс показывает фактические `promptTokenCount`, `responseTokenCount` и `totalTokenCount` из `usageMetadata`. Денежную стоимость нужно сверять в Google Billing: тариф не зашит в приложение, чтобы интерфейс не показывал устаревшую цену preview-модели.

## Проверка

```bash
npm test
node --check server.js
node --check public/app.js
```

Документация SDK: [Google Gen AI SDK for JavaScript](https://googleapis.github.io/js-genai/).

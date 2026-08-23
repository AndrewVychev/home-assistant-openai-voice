# Home Voice Satellite Protocol v1

Один протокол используется Android, Go-сателлитом и будущим ESP32-S3/ReSpeaker. Облачные ключи и токен Home Assistant никогда не передаются сателлиту.

## Соединение

Для ручной активации используется `/live`; постоянный server-side wake использует `/satellite`:

```text
GET ws(s)://gateway:3000/satellite
    ?protocol=1
    &transport=binary
    &provider=openai|gemini
    &response_mode=audio|text
    &satellite_id=living-room
Authorization: Bearer <SATELLITE_TOKEN>
```

`SATELLITE_TOKEN` на gateway опционален только для локальной разработки. Для постоянной установки он обязателен. `satellite_id` стабилен и задаёт область краткосрочного контекста: контекст одной комнаты не протекает в другую.

## Аудио

- Satellite → Gateway: WebSocket binary frame, PCM signed 16-bit little-endian, mono, 16 kHz.
- Gateway → Satellite: WebSocket binary frame, PCM signed 16-bit little-endian, mono, 24 kHz.
- Рекомендуемый входной frame: 20 ms = 640 bytes.
- Микрофон выключается на время воспроизведения ответа, поэтому протокол сейчас half-duplex и не требует полноценного AEC.

В ручном `/live` после подключения gateway присылает текстовый JSON:

```json
{
  "type": "ready",
  "protocolVersion": 1,
  "transport": "binary",
  "provider": "openai",
  "model": "gpt-realtime-2.1-mini",
  "responseMode": "audio",
  "inputAudio": { "encoding": "pcm_s16le", "sampleRate": 16000, "channels": 1 },
  "outputAudio": { "encoding": "pcm_s16le", "sampleRate": 24000, "channels": 1 }
}
```

В постоянном `/satellite` gateway сначала присылает `wake_ready`; после него сателлит непрерывно отправляет PCM. При распознавании приходят `wake_detected` и затем `live_ready`. Управляющие и диагностические события остаются JSON text frames: `input_transcript`, `output_transcript`, `tool_call`, `ha_result`, `turn_complete`, `error`.

Старый `json-base64` транспорт временно поддерживается Go CLI для обратной совместимости.

## Жизненный цикл

1. Сателлит открывает `/satellite`, получает `wake_ready` и непрерывно отправляет PCM по локальной сети.
2. Gateway распознаёт «Кузу» локально и отбрасывает wake-аудио, чтобы оно не стало отдельным VAD-turn модели.
3. После wake gateway присылает `wake_detected`, подключает Live provider и в это время буферизует все последующие PCM-фреймы команды.
4. Gateway исполняет разрешённый tool call и возвращает ответ.
5. На первом ответном аудио сателлит глушит микрофон и воспроизводит PCM.
6. После `turn_complete` gateway закрывает только облачную сессию и возвращает постоянное соединение в `wake_ready`. При уточняющем вопросе Live-сессия остаётся открытой.

## Совместимость

Новые несовместимые форматы получают новый `protocol` query parameter. Неизвестную версию gateway должен отклонять до открытия облачной сессии. Возможности конкретного железа в дальнейшем объявляются отдельным `hello` event; формат аудио v1 при этом не меняется.
